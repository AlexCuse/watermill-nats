package nats_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill-nats/v2/pkg/msg"
	wmnats "github.com/ThreeDotsLabs/watermill-nats/v2/pkg/nats"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/pubsub/tests"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func getTestFeatures() tests.Features {
	return tests.Features{
		ConsumerGroups:                      false,
		ExactlyOnceDelivery:                 false,
		GuaranteedOrder:                     false,
		GuaranteedOrderWithSingleSubscriber: true,
		Persistent:                          false,
		RequireSingleInstance:               false,
		NewSubscriberReceivesOldMessages:    false,
	}
}

func newPubSub(t *testing.T, clientID string, queueName string, exactlyOnce bool) (message.Publisher, message.Subscriber) {
	trace := os.Getenv("WATERMILL_TEST_NATS_TRACE")
	debug := os.Getenv("WATERMILL_TEST_NATS_DEBUG")

	format := os.Getenv("WATERMILL_TEST_NATS_FORMAT")
	marshaler := msg.GetMarshaler(format)

	logger := watermill.NewStdLogger(strings.ToLower(debug) == "true", strings.ToLower(trace) == "true")

	natsURL := os.Getenv("WATERMILL_TEST_NATS_URL")
	if natsURL == "" {
		natsURL = nats.DefaultURL
	}

	options := []nats.Option{
		nats.RetryOnFailedConnect(true),
		nats.Timeout(30 * time.Second),
		nats.ReconnectWait(1 * time.Second),
		nats.Name(clientID),
	}

	subscriberCount := 1

	if queueName != "" {
		subscriberCount = 2
	}

	c, err := nats.Connect(natsURL, options...)
	require.NoError(t, err)

	defer c.Close()

	pub, err := wmnats.NewPublisher(wmnats.PublisherConfig{
		URL:         natsURL,
		Marshaler:   marshaler,
		NatsOptions: options,
	}, logger)
	require.NoError(t, err)

	sub, err := wmnats.NewSubscriber(wmnats.SubscriberConfig{
		URL:              natsURL,
		QueueGroup:       queueName,
		SubscribersCount: subscriberCount, //multiple only works if a queue group specified
		AckWaitTimeout:   30 * time.Second,
		Unmarshaler:      marshaler,
		NatsOptions:      options,
		CloseTimeout:     30 * time.Second,
		AckSync:          exactlyOnce,
	}, logger)
	require.NoError(t, err)

	return pub, sub
}

func createPubSub(t *testing.T) (message.Publisher, message.Subscriber) {
	return newPubSub(t, watermill.NewUUID(), "", false)
}

func createPubSubWithConsumerGroup(t *testing.T, consumerGroup string) (message.Publisher, message.Subscriber) {
	return newPubSub(t, watermill.NewUUID(), consumerGroup, false)
}

//nolint:deadcode,unused
func createPubSubWithExactlyOnce(t *testing.T) (message.Publisher, message.Subscriber) {
	return newPubSub(t, watermill.NewUUID(), "", true)
}

//nolint:deadcode,unused
func createPubSubWithConsumerGroupWithExactlyOnce(t *testing.T, consumerGroup string) (message.Publisher, message.Subscriber) {
	return newPubSub(t, watermill.NewUUID(), consumerGroup, true)
}
