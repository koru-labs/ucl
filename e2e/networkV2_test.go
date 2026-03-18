package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/e2e/frameworkV2"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestE2E_NetworkDiscoveryProtocol(t *testing.T) {
	const (
		validatorCount    = 5
		nonValidatorCount = 5
		// each node in cluster should find at least 2 more peers beside bootnode
		atLeastPeers = 3
		testTimeout  = time.Second * 60
	)

	// create cluster
	cluster := frameworkV2.NewTestCluster(t, validatorCount,
		frameworkV2.WithNonValidators(nonValidatorCount),
		frameworkV2.WithBootnodeCount(1))
	defer cluster.Stop()

	ctx := context.Background()

	// wait for everyone to have at least 'atLeastPeers' peers
	err := cluster.WaitForGeneric(testTimeout, func(ts *frameworkV2.TestServer) bool {
		peerList, err := ts.Conn().PeersList(ctx, &emptypb.Empty{})

		return err == nil && len(peerList.GetPeers()) >= atLeastPeers
	})
	assert.NoError(t, err)
}
