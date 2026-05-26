package ibft

import (
	"github.com/0xPolygon/go-ibft/messages/proto"
	"github.com/0xPolygon/polygon-edge/network"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/libp2p/go-libp2p/core/peer"
)

type transport interface {
	Multicast(msg *proto.IbftMessage) error
}

type gossipTransport struct {
	topic *network.Topic
}

func (g *gossipTransport) Multicast(msg *proto.IbftMessage) error {
	return g.topic.Publish(msg)
}

func (i *backendIBFT) Multicast(msg *proto.IbftMessage) {
	view := msg.GetView()

	i.logger.Info(
		"consensus message sent",
		"type", msg.Type.String(),
		"height", view.GetHeight(),
		"round", view.GetRound(),
		"from_validator", types.BytesToAddress(msg.From).String(),
	)

	if err := i.transport.Multicast(msg); err != nil {
		i.logger.Error("fail to gossip", "err", err)
	}
}

// setupTransport sets up the gossip transport protocol
func (i *backendIBFT) setupTransport() error {
	// Define a new topic
	topic, err := i.network.NewTopic(ibftProto, &proto.IbftMessage{})
	if err != nil {
		return err
	}

	// Subscribe to the newly created topic. Both validators and sentries log
	// the inbound message so we can see propagation across the whole network,
	// but only validators forward the message to the consensus state machine.
	if err := topic.Subscribe(
		func(obj interface{}, from peer.ID) {
			msg, ok := obj.(*proto.IbftMessage)
			if !ok {
				i.logger.Error("invalid type assertion for message request")

				return
			}

			isActive := i.isActiveValidator()

			role := "sentry"
			if isActive {
				role = "validator"
			}

			i.logger.Info(
				"consensus message received",
				"role", role,
				"type", msg.Type.String(),
				"height", msg.GetView().Height,
				"round", msg.GetView().Round,
				"from_validator", types.BytesToAddress(msg.From).String(),
				"forwarded_by", from.String(),
			)

			if !isActive {
				return
			}

			i.consensus.AddMessage(msg)
		},
	); err != nil {
		return err
	}

	i.transport = &gossipTransport{topic: topic}

	return nil
}
