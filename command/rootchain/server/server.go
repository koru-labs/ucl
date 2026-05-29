package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	mobyClient "github.com/moby/moby/client"
	"github.com/spf13/cobra"

	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/command/rootchain/helper"
	"github.com/0xPolygon/polygon-edge/helper/common"
)

const (
	gethConsoleImage = "ghcr.io/0xpolygon/go-ethereum-console:latest"
	gethImage        = "ethereum/client-go:v1.9.25"

	defaultHostIP   = "127.0.0.1"
	defaultHostPort = "8545"
)

var (
	params            serverParams
	client            *mobyClient.Client
	dockerContainerID string
)

// GetCommand returns the rootchain server command
func GetCommand() *cobra.Command {
	rootchainServerCmd := &cobra.Command{
		Use:     "server",
		Short:   "Start the rootchain command",
		PreRunE: runPreRun,
		Run:     runCommand,
	}

	setFlags(rootchainServerCmd)

	return rootchainServerCmd
}

func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&params.dataDir,
		dataDirFlag,
		"test-rootchain",
		"target directory for the chain",
	)

	cmd.Flags().BoolVar(
		&params.noConsole,
		noConsole,
		false,
		"use the official geth image instead of the console fork",
	)
}

func runPreRun(_ *cobra.Command, _ []string) error {
	return nil
}

func runCommand(cmd *cobra.Command, _ []string) {
	ctx := cmd.Context()

	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	closeCh := make(chan struct{})

	// Check if the client is already running
	if cid, err := helper.GetRootchainID(); !errors.Is(err, helper.ErrRootchainNotFound) {
		if err != nil {
			outputter.SetError(err)
		} else if cid != "" {
			outputter.SetError(fmt.Errorf("rootchain already running: %s", cid))
		}

		return
	}

	// Start the client
	if err := runRootchain(ctx, outputter, closeCh); err != nil {
		outputter.SetError(fmt.Errorf("failed to run rootchain: %w", err))

		return
	}

	// Ping geth server to make sure everything is up and running
	if err := PingServer(closeCh); err != nil {
		close(closeCh)

		if ip, err := helper.ReadRootchainIP(); err != nil {
			outputter.SetError(fmt.Errorf("failed to ping rootchain server: %w", err))
		} else {
			outputter.SetError(fmt.Errorf("failed to ping rootchain server at address %s: %w", ip, err))
		}

		return
	}

	// Gather the logs
	go func() {
		if err := gatherLogs(ctx, outputter); err != nil {
			outputter.SetError(fmt.Errorf("failed to gether logs: %w", err))

			return
		}
	}()

	if err := handleSignals(ctx, closeCh); err != nil {
		outputter.SetError(fmt.Errorf("failed to handle signals: %w", err))
	}
}

func runRootchain(ctx context.Context, outputter command.OutputFormatter, closeCh chan struct{}) error {
	var err error
	if client, err = mobyClient.NewClientWithOpts(mobyClient.FromEnv,
		mobyClient.WithAPIVersionNegotiation()); err != nil {
		return err
	}

	// target directory for the chain
	if err = common.CreateDirSafe(params.dataDir, 0700); err != nil {
		return err
	}

	image := gethConsoleImage
	if params.noConsole {
		image = gethImage
	}

	// try to pull the image
	reader, err := client.ImagePull(ctx, image, mobyClient.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()

	if _, err = io.Copy(outputter, reader); err != nil {
		return fmt.Errorf("cannot copy: %w", err)
	}

	// create the client
	args := []string{"--dev"}

	// add period of 2 seconds
	args = append(args, "--dev.period", "2")

	// add data dir
	args = append(args, "--datadir", "/eth1data")

	// add ipcpath
	args = append(args, "--ipcpath", "/eth1data/geth.ipc")

	// enable rpc
	args = append(args, "--http", "--http.addr", "0.0.0.0", "--http.api", "eth,net,web3,debug")

	// enable ws
	args = append(args, "--ws", "--ws.addr", "0.0.0.0")

	config := &container.Config{
		Image: image,
		Cmd:   args,
		Labels: map[string]string{
			"edge-type": "rootchain",
		},
	}

	mountDir := params.dataDir

	// we need to use the full path
	if !strings.HasPrefix(params.dataDir, "/") {
		// if the path is not absolute, assume we want to create it locally
		// in current folder
		pwdDir, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		} else {
			mountDir = filepath.Join(pwdDir, params.dataDir)
		}
	}

	port, err := network.ParsePort(fmt.Sprintf("%s/tcp", defaultHostPort))
	if err != nil {
		return fmt.Errorf("failed to parse port: %w", err)
	}

	hostConfig := &container.HostConfig{
		Binds: []string{
			mountDir + ":/eth1data",
		},
		PortBindings: network.PortMap{
			port: []network.PortBinding{
				{
					HostIP:   netip.AddrFrom4([4]byte{127, 0, 0, 1}),
					HostPort: defaultHostPort,
				},
			},
		},
	}

	options := mobyClient.ContainerCreateOptions{
		Config:     config,
		HostConfig: hostConfig,
	}

	resp, err := client.ContainerCreate(ctx, options)
	if err != nil {
		return err
	}

	// start the client
	_, err = client.ContainerStart(ctx, resp.ID, mobyClient.ContainerStartOptions{})
	if err != nil {
		return err
	}

	dockerContainerID = resp.ID

	// wait for it to finish
	go func() {
		container := client.ContainerWait(ctx, dockerContainerID,
			mobyClient.ContainerWaitOptions{
				Condition: container.WaitConditionNotRunning,
			})
		select {
		case err = <-container.Error:
			outputter.SetError(err)
		case status := <-container.Result:
			outputter.SetCommandResult(newContainerStopResult(status))
		}
		close(closeCh)
	}()

	return nil
}

func gatherLogs(ctx context.Context, outputter command.OutputFormatter) error {
	opts := mobyClient.ContainerLogsOptions{
		ShowStderr: true,
		ShowStdout: true,
		Follow:     true,
	}

	out, err := client.ContainerLogs(ctx, dockerContainerID, opts)
	if err != nil {
		return fmt.Errorf("failed to retrieve container logs: %w", err)
	}

	if _, err = stdcopy.StdCopy(outputter, outputter, out); err != nil {
		return fmt.Errorf("failed to write container logs to the stdout: %w", err)
	}

	return nil
}

func PingServer(closeCh <-chan struct{}) error {
	httpTimer := time.NewTimer(30 * time.Second)
	httpClient := http.Client{
		Timeout: 5 * time.Second,
	}

	for {
		select {
		case <-time.After(500 * time.Millisecond):
			resp, err := httpClient.Post(fmt.Sprintf("http://%s:%s", defaultHostIP, defaultHostPort), "application/json", nil)
			if err == nil {
				return resp.Body.Close()
			}
		case <-httpTimer.C:
			return fmt.Errorf("timeout to start http")
		case <-closeCh:
			return fmt.Errorf("closed before connecting with http. Is there any other process running and using rootchain dir?")
		}
	}
}

func handleSignals(ctx context.Context, closeCh <-chan struct{}) error {
	signalCh := make(chan os.Signal, 4)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	stop := true
	select {
	case <-signalCh:
	case <-closeCh:
		stop = false
	}

	// close the container if possible
	if stop {
		_, err := client.ContainerStop(ctx, dockerContainerID, mobyClient.ContainerStopOptions{})
		if err != nil {
			return fmt.Errorf("failed to stop container: %w", err)
		}
	}

	return nil
}
