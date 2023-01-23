package nats

import (
	"context"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	watermillSync "github.com/ThreeDotsLabs/watermill/pubsub/sync"
	"github.com/nats-io/nats.go"
	"github.com/pkg/errors"
)

// SubscriberConfig is the configuration to create a subscriber
type SubscriberConfig struct {
	// URL is the URL to the broker
	URL string

	// QueueGroup is the queue group.
	//
	// All subscriptions with the same queue name (regardless of the connection they originate from)
	// will form a queue group. Each message will be delivered to only one subscriber per queue group,
	// using queuing semantics.
	//
	// For JetStream is recommended to set it with DurableName.
	// For non durable queue subscribers, when the last member leaves the group,
	// that group is removed. A durable queue group (DurableName) allows you to have all members leave
	// but still maintain state. When a member re-joins, it starts at the last position in that group.
	//
	// When QueueGroup is empty, subscribe without QueueGroup will be used.
	QueueGroup string

	// SubscribersCount determines how many concurrent subscribers should be started.
	SubscribersCount int

	// CloseTimeout determines how long subscriber will wait for Ack/Nack on close.
	// When no Ack/Nack is received after CloseTimeout, subscriber will be closed.
	CloseTimeout time.Duration

	// How long subscriber should wait for Ack/Nack. When no Ack/Nack was received, message will be redelivered.
	// It is mapped to stan.AckWait option.
	AckWaitTimeout time.Duration

	// SubscribeTimeout determines how long subscriber will wait for a successful subscription
	SubscribeTimeout time.Duration

	// NatsOptions are custom []nats.Option passed to the connection.
	// It is also used to provide connection parameters, for example:
	// 		nats.URL("nats://localhost:4222")
	NatsOptions []nats.Option

	// Unmarshaler is an unmarshaler used to unmarshaling messages from NATS format to Watermill format.
	Unmarshaler Unmarshaler

	// SubjectCalculator is a function used to transform a topic to an array of subjects on creation (defaults to "{topic}.*")
	SubjectCalculator SubjectCalculator

	// AckSync enables synchronous acknowledgement (needed for exactly once processing)
	AckSync bool

	// NakDelay sets duration after which the NACKed message will be resent.
	// By default, it's NACKed without delay.
	NakDelay Delay

	// JetStream holds JetStream specific settings
	JetStream JetStreamConfig
}

// SubscriberSubscriptionConfig is the configurationz
type SubscriberSubscriptionConfig struct {
	// Unmarshaler is an unmarshaler used to unmarshaling messages from NATS format to Watermill format.
	Unmarshaler Unmarshaler

	// QueueGroup is the queue group.
	//
	// All subscriptions with the same queue name (regardless of the connection they originate from)
	// will form a queue group. Each message will be delivered to only one subscriber per queue group,
	// using queuing semantics.
	//
	// For JetStream is recommended to set it with DurableName.
	// For non durable queue subscribers, when the last member leaves the group,
	// that group is removed. A durable queue group (DurableName) allows you to have all members leave
	// but still maintain state. When a member re-joins, it starts at the last position in that group.
	//
	// When QueueGroup is empty, subscribe without QueueGroup will be used.
	QueueGroup string

	// SubscribersCount determines wow much concurrent subscribers should be started.
	SubscribersCount int

	// How long subscriber should wait for Ack/Nack. When no Ack/Nack was received, message will be redelivered.
	// It is mapped to stan.AckWait option.
	AckWaitTimeout time.Duration

	// CloseTimeout determines how long subscriber will wait for Ack/Nack on close.
	// When no Ack/Nack is received after CloseTimeout, subscriber will be closed.
	CloseTimeout time.Duration

	// SubscribeTimeout determines how long subscriber will wait for a successful subscription
	SubscribeTimeout time.Duration

	// SubjectCalculator is a function used to transform a topic to an array of subjects on creation (defaults to "{topic}.*")
	SubjectCalculator SubjectCalculator

	// AckSync enables synchronous acknowledgement (needed for exactly once processing)
	AckSync bool

	// NakDelay sets duration after which the NACKed message will be resent.
	// By default, it's NACKed without delay.
	NakDelay Delay

	// JetStream holds JetStream specific settings
	JetStream JetStreamConfig
}

// GetSubscriberSubscriptionConfig gets the configuration subset needed for individual subscribe calls once a connection has been established
func (c *SubscriberConfig) GetSubscriberSubscriptionConfig() SubscriberSubscriptionConfig {
	return SubscriberSubscriptionConfig{
		Unmarshaler:       c.Unmarshaler,
		QueueGroup:        c.QueueGroup,
		SubscribersCount:  c.SubscribersCount,
		AckWaitTimeout:    c.AckWaitTimeout,
		CloseTimeout:      c.CloseTimeout,
		SubscribeTimeout:  c.SubscribeTimeout,
		SubjectCalculator: c.SubjectCalculator,
		AckSync:           c.AckSync,
		NakDelay:          c.NakDelay,
		JetStream:         c.JetStream,
	}
}

func (c *SubscriberSubscriptionConfig) setDefaults() {
	if c.SubscribersCount <= 0 {
		c.SubscribersCount = 1
	}
	if c.CloseTimeout <= 0 {
		c.CloseTimeout = time.Second * 30
	}
	if c.AckWaitTimeout <= 0 {
		c.AckWaitTimeout = time.Second * 30
	}
	if c.SubscribeTimeout <= 0 {
		c.SubscribeTimeout = time.Second * 30
	}

	if c.Unmarshaler == nil {
		c.Unmarshaler = &NATSMarshaler{}
	}

	if c.SubjectCalculator == nil {
		c.SubjectCalculator = DefaultSubjectCalculator
	}
}

// Validate ensures configuration is valid before use
func (c *SubscriberSubscriptionConfig) Validate() error {
	if c.Unmarshaler == nil {
		return errors.New("SubscriberConfig.Unmarshaler is missing")
	}

	if c.QueueGroup == "" && c.SubscribersCount > 1 {
		return errors.New(
			"to set SubscriberConfig.SubscribersCount " +
				"you need to also set SubscriberConfig.QueueGroup, " +
				"in other case you will receive duplicated messages",
		)
	}

	if c.SubjectCalculator == nil {
		return errors.New("SubscriberSubscriptionConfig.SubjectCalculator is required.")
	}

	return nil
}

// Subscriber provides the nats implementation for watermill subscribe operations
type Subscriber struct {
	conn   Connection
	logger watermill.LoggerAdapter

	config SubscriberSubscriptionConfig

	subsLock sync.RWMutex

	closed  bool
	closing chan struct{}

	outputsWg        sync.WaitGroup
	topicInterpreter *topicInterpreter
}

// NewSubscriber creates a new Subscriber.
func NewSubscriber(config SubscriberConfig, logger watermill.LoggerAdapter) (*Subscriber, error) {
	conn, err := nats.Connect(config.URL, config.NatsOptions...)
	if err != nil {
		return nil, errors.Wrap(err, "cannot connect to NATS")
	}
	return NewSubscriberWithNatsConn(conn, config.GetSubscriberSubscriptionConfig(), logger)
}

// NewSubscriberWithNatsConn creates a new Subscriber with the provided nats-core connection.
func NewSubscriberWithNatsConn(conn *nats.Conn, config SubscriberSubscriptionConfig, logger watermill.LoggerAdapter) (*Subscriber, error) {
	config.setDefaults()

	if err := config.Validate(); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = watermill.NopLogger{}
	}

	var connection Connection = conn
	var interpreter *topicInterpreter

	if config.JetStream.Enabled {
		js, err := conn.JetStream(config.JetStream.ConnectOptions...)

		connection = &jsConnection{conn, js, config.JetStream}

		if err != nil {
			return nil, err
		}

		interpreter = newTopicInterpreter(js, config.SubjectCalculator)
	}

	return &Subscriber{
		conn:             connection,
		logger:           logger,
		config:           config,
		closing:          make(chan struct{}),
		topicInterpreter: interpreter,
	}, nil
}

// Subscribe subscribes messages from JetStream.
func (s *Subscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	output := make(chan *message.Message)

	s.outputsWg.Add(1)
	outputWg := &sync.WaitGroup{}

	for i := 0; i < s.config.SubscribersCount; i++ {
		outputWg.Add(1)

		subscriberLogFields := watermill.LogFields{
			"subscriber_num": i,
			"topic":          topic,
		}

		s.logger.Debug("Starting subscriber", subscriberLogFields)

		sub, err := s.subscribe(topic, func(msg *nats.Msg) {
			s.processMessage(ctx, msg, output, subscriberLogFields)
		})
		if err != nil {
			return nil, errors.Wrap(err, "cannot subscribe")
		}

		go func(subscriber *nats.Subscription, subscriberLogFields watermill.LogFields) {
			defer outputWg.Done()
			select {
			case <-s.closing:
				// unblock
			case <-ctx.Done():
				// unblock
			}

			if err := sub.Unsubscribe(); err != nil {
				s.logger.Error("Cannot unsubscribe", err, subscriberLogFields)
			}
		}(sub, subscriberLogFields)
	}

	go func() {
		defer s.outputsWg.Done()
		outputWg.Wait()
		close(output)
	}()

	return output, nil
}

// SubscribeInitialize offers a way to ensure the stream for a topic exists prior to subscribe
func (s *Subscriber) SubscribeInitialize(topic string) error {
	err := s.topicInterpreter.ensureStream(topic)

	if err != nil {
		return errors.Wrap(err, "cannot initialize subscribe")
	}

	return nil
}

func (s *Subscriber) subscribe(topic string, cb nats.MsgHandler) (*nats.Subscription, error) {
	if s.config.JetStream.Enabled && s.config.JetStream.AutoProvision {
		err := s.SubscribeInitialize(topic)
		if err != nil {
			return nil, err
		}
	}

	primarySubject := s.config.SubjectCalculator(topic).Primary

	return s.conn.QueueSubscribe(
		primarySubject,
		s.config.QueueGroup,
		cb,
	)
}

func (s *Subscriber) processMessage(
	ctx context.Context,
	m *nats.Msg,
	output chan *message.Message,
	logFields watermill.LogFields,
) {
	s.logger.Trace("Received message", logFields)

	msg, err := s.config.Unmarshaler.Unmarshal(m)
	if err != nil {
		s.logger.Error("Cannot unmarshal message", err, logFields)
		return
	}

	messageLogFields := logFields.Add(watermill.LogFields{"message_uuid": msg.UUID})
	s.logger.Trace("Unmarshaled message", messageLogFields)

	ctx, cancelCtx := context.WithCancel(ctx)
	msg.SetContext(ctx)
	defer cancelCtx()

	timeout := time.NewTimer(s.config.AckWaitTimeout)
	defer timeout.Stop()

	if s.isClosed() {
		return
	}

	select {
	case <-s.closing:
		s.logger.Trace("Closing, message discarded", messageLogFields)
		return
	case <-ctx.Done():
		s.logger.Trace("Context cancelled, message discarded", messageLogFields)
		return
	// if this is first can risk 'send on closed channel' errors
	case output <- msg:
		s.logger.Trace("Message sent to consumer", messageLogFields)
	}

	select {
	case <-msg.Acked():
		if m.Reply == "" {
			s.logger.Trace("ack without a reply subject is a no-op", messageLogFields)
			return
		}
		var err error

		if s.config.AckSync {
			err = m.AckSync()
		} else {
			err = m.Ack()
		}

		if err != nil {
			s.logger.Error("Cannot send ack", err, messageLogFields)
			return
		}
		s.logger.Trace("Message Acked", messageLogFields)
	case <-msg.Nacked():
		if m.Reply == "" {
			s.logger.Trace("Ignoring nack without reply topic", messageLogFields)
			return
		}

		var nakDelay time.Duration

		if s.config.NakDelay != nil {
			metadata, err := m.Metadata()
			if err != nil {
				s.logger.Error("Cannot parse nats message metadata, use nak without delay", err, messageLogFields)
			} else {
				nakDelay = s.config.NakDelay.WaitTime(metadata.NumDelivered)
				messageLogFields = messageLogFields.Add(watermill.LogFields{
					"delay":    nakDelay.String(),
					"retryNum": metadata.NumDelivered,
				})
			}
		}

		if nakDelay == StopTime {
			if err := m.Term(); err != nil {
				s.logger.Error("Cannot send term", err, messageLogFields)
			} else {
				s.logger.Trace("Message Termed via -1 NakDelay calculation", messageLogFields)
			}
			return
		}

		if nakDelay > 0 {
			if err := m.NakWithDelay(nakDelay); err != nil {
				s.logger.Error("Cannot send nak", err, messageLogFields)
				return
			}
		} else {
			if err := m.Nak(); err != nil {
				s.logger.Error("Cannot send nak", err, messageLogFields)
				return
			}
		}
		s.logger.Trace("Message Nacked", messageLogFields)
		return
	case <-timeout.C:
		s.logger.Trace("Ack timeout", messageLogFields)
		return
	case <-s.closing:
		s.logger.Trace("Closing, message discarded before ack", messageLogFields)
		return
	case <-ctx.Done():
		s.logger.Trace("Context cancelled, message discarded before ack", messageLogFields)
		return
	}
}

// Close closes the publisher and the underlying connection.  It will attempt to wait for in-flight messages to complete.
func (s *Subscriber) Close() error {
	s.subsLock.Lock()
	defer s.subsLock.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	s.logger.Debug("Closing subscriber", nil)
	defer s.logger.Info("Subscriber closed", nil)

	close(s.closing)

	if watermillSync.WaitGroupTimeout(&s.outputsWg, s.config.CloseTimeout) {
		return errors.New("output wait group did not finish")
	}

	if err := s.conn.Drain(); err != nil {
		return errors.Wrap(err, "cannot close conn")
	}

	return nil
}

func (s *Subscriber) isClosed() bool {
	s.subsLock.RLock()
	defer s.subsLock.RUnlock()

	return s.closed
}
