package goneli

import (
	"sync"
	"testing"
	"time"

	"github.com/obsidiandynamics/libstdgo/check"
	"github.com/obsidiandynamics/libstdgo/scribe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/confluentinc/confluent-kafka-go.v1/kafka"
)

func wait(t check.Tester) check.Timesert {
	return check.Wait(t, 10*time.Second)
}

type fixtures struct{}

func (fixtureOpts fixtures) create() (scribe.MockScribe, *consMock, Config, *testEventHandler) {
	m := scribe.NewMock()

	cons := &consMock{}
	cons.fillDefaults()

	config := Config{
		KafkaConsumerProvider: mockKafkaConsumerProvider(cons),
		MinPollInterval:       Duration(1 * time.Millisecond),
		PollDuration:          Duration(1 * time.Millisecond),
		Scribe:                scribe.New(m.Loggers()),
	}
	config.Scribe.SetEnabled(scribe.All)

	return m, cons, config, &testEventHandler{}
}

type handler interface {
	handler() EventHandler
	list() []Event
	length() int
}

type testEventHandler struct {
	lock   sync.Mutex
	events []Event
}

func (c *testEventHandler) handler() EventHandler {
	return func(e Event) {
		c.lock.Lock()
		defer c.lock.Unlock()
		c.events = append(c.events, e)
	}
}

func (c *testEventHandler) list() []Event {
	c.lock.Lock()
	defer c.lock.Unlock()
	eventsCopy := make([]Event, len(c.events))
	copy(eventsCopy, c.events)
	return eventsCopy
}

func (c *testEventHandler) length() int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return len(c.events)
}

func TestCorrectInitialisation(t *testing.T) {
	_, cons, config, eh := fixtures{}.create()

	var givenConsumerConfig *KafkaConfigMap
	var givenLeaderTopic string

	cons.f.Subscribe = func(m *consMock, topic string, rebalanceCb kafka.RebalanceCb) error {
		givenLeaderTopic = topic
		return nil
	}
	config.KafkaConsumerProvider = func(conf *KafkaConfigMap) (KafkaConsumer, error) {
		givenConsumerConfig = conf
		return cons, nil
	}
	config.LeaderTopic = "test leader topic"
	config.LeaderGroupID = "test leader group ID"
	config.KafkaConfig = KafkaConfigMap{
		"bootstrap.servers": "localhost:9092",
	}

	h, err := New(config, eh.handler())
	require.Nil(t, err)
	assert.Equal(t, Live, h.State())

	assert.Equal(t, config.LeaderTopic, givenLeaderTopic)
	assert.Contains(t, *givenConsumerConfig, "bootstrap.servers")
	assert.Equal(t, 1, cons.c.Subscribe.GetInt())

	assert.Nil(t, h.Close())
	h.Await()
	assert.Equal(t, Closed, h.State())

	assert.Equal(t, 1, cons.c.Close.GetInt())
}

func TestConfigError(t *testing.T) {
	h, err := New(Config{
		MinPollInterval: Duration(0),
	}, (&testEventHandler{}).handler())
	assert.Nil(t, h)
	assert.NotNil(t, err)
}

/*
func TestErrorDuringDBInitialisation(t *testing.T) {
	_, _, _, config := fixtures{}.create()

	config.DatabaseBindingProvider = func(dataSource string, outboxTable string) (DatabaseBinding, error) {
		return nil, check.ErrSimulated
	}
	h, err := New(config)
	require.Nil(t, err)

	assertErrorContaining(t, h.Start, "simulated")
	assert.Equal(t, Created, h.State())
}

func TestErrorDuringConsumerInitialisation(t *testing.T) {
	_, db, _, config := fixtures{}.create()

	config.KafkaConsumerProvider = func(conf *KafkaConfigMap) (KafkaConsumer, error) {
		return nil, check.ErrSimulated
	}
	h, err := New(config)
	require.Nil(t, err)

	assertErrorContaining(t, h.Start, "simulated")
	assert.Equal(t, Created, h.State())
	assert.Equal(t, 1, db.c.Dispose.GetInt())
}

func TestErrorDuringConsumerConfiguration(t *testing.T) {
	_, _, _, config := fixtures{}.create()

	config.BaseKafkaConfig = KafkaConfigMap{
		"group.id": "overridden_group",
	}
	h, err := New(config)
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "Cannot override configuration 'group.id'")
	assert.Nil(t, h)
}

func TestErrorDuringProducerConfiguration(t *testing.T) {
	_, _, _, config := fixtures{}.create()

	config.ProducerKafkaConfig = KafkaConfigMap{
		"enable.idempotence": false,
	}
	h, err := New(config)
	require.NotNil(t, err)
	assert.Contains(t, err.Error(), "Cannot override configuration 'enable.idempotence'")
	assert.Nil(t, h)
}

func TestErrorDuringProducerInitialisation(t *testing.T) {
	m, db, cons, config := fixtures{}.create()

	config.KafkaProducerProvider = func(conf *KafkaConfigMap) (KafkaProducer, error) {
		return nil, check.ErrSimulated
	}
	h, err := New(config)
	require.Nil(t, err)

	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Induce leadership and await
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).Until(h.IsLeader)
	wait(t).UntilAsserted(func(t check.Tester) {
		assert.Equal(t, 1, eh.length())
	})

	// Wait for at least one message to be consumed.
	wait(t).UntilAsserted(atLeast(1, cons.c.ReadMessage.GetInt))

	m.Entries().
		Having(scribe.LogLevel(scribe.Info)).
		Having(scribe.MessageEqual("Starting background poller")).
		Assert(t, scribe.Count(1))

	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Error)).
		Having(scribe.MessageEqual("Caught panic in send cell: simulated")).
		Passes(scribe.Count(1)))

	// Having detected a panic, it should self-destruct
	assertErrorContaining(t, h.Await, "simulated")

	assert.Equal(t, 1, db.c.Dispose.GetInt())
	assert.Equal(t, 1, cons.c.Close.GetInt())
}

func TestErrorDuringSubscribe(t *testing.T) {
	_, db, cons, config := fixtures{}.create()

	h, err := New(config)
	require.Nil(t, err)
	cons.f.Subscribe = func(m *consMock, topic string, rebalanceCb kafka.RebalanceCb) error {
		return check.ErrSimulated
	}
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())

	assert.Equal(t, Created, h.State())

	assertErrorContaining(t, h.Start, "simulated")
	assert.Equal(t, Created, h.State())
	assert.Equal(t, 1, db.c.Dispose.GetInt())
	assert.Equal(t, 1, cons.c.Close.GetInt())
	assert.Equal(t, 0, eh.length())
}

func TestUncaughtPanic_backgroundPoller(t *testing.T) {
	m, _, cons, config := fixtures{}.create()

	cons.f.ReadMessage = func(m *consMock, timeout time.Duration) (*kafka.Message, error) {
		return nil, kafka.NewError(kafka.ErrFatal, "simulated", true)
	}

	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Wait for at least one cycle before stopping
	wait(t).UntilAsserted(atLeast(1, cons.c.ReadMessage.GetInt))

	// Having detected a panic, it should self-destruct
	assertErrorContaining(t, h.Await, "simulated")
	assert.Equal(t, 0, eh.length())

	t.Log(m.Entries().List())
	m.Entries().
		Having(scribe.LogLevel(scribe.Info)).
		Having(scribe.MessageEqual("Starting background poller")).
		Assert(t, scribe.Count(1))

	m.Entries().
		Having(scribe.LogLevel(scribe.Error)).
		Having(scribe.MessageEqual("Caught panic in background poller: Fatal error: simulated")).
		Assert(t, scribe.Count(1))

	m.Entries().
		Having(scribe.LogLevel(scribe.Trace)).
		Having(scribe.MessageEqual("Polling... (leader ID: <nil>)")).
		Assert(t, scribe.Count(1))
}

func TestUncaughtPanic_backgroundDeliveryHandler(t *testing.T) {
	prodRef := concurrent.NewAtomicReference()
	m, db, cons, config := fixtures{producerMockSetup: func(prodMock *prodMock) {
		prodRef.Set(prodMock)
	}}.create()

	db.f.Reset = func(m *dbMock, id int64) (bool, error) {
		panic(check.ErrSimulated)
	}

	h, err := New(config)
	require.Nil(t, err)
	assertNoError(t, h.Start)

	// Induce leadership and await
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).Until(h.IsLeader)

	// Feed a delivery event to cause a DB reset query
	wait(t).UntilAsserted(isNotNil(prodRef.Get))
	prodRef.Get().(*prodMock).events <- message(OutboxRecord{ID: 777}, check.ErrSimulated)

	// Having detected a panic, it should self-destruct
	assertErrorContaining(t, h.Await, "simulated")

	t.Log(m.Entries().List())
	m.Entries().
		Having(scribe.LogLevel(scribe.Info)).
		Having(scribe.MessageEqual("Starting background delivery handler")).
		Assert(t, scribe.Count(1))

	m.Entries().
		Having(scribe.LogLevel(scribe.Error)).
		Having(scribe.MessageEqual("Caught panic in background delivery handler: simulated")).
		Assert(t, scribe.Count(1))
}

func TestBasicLeadershipElectionAndRevocation(t *testing.T) {
	m, _, cons, config := fixtures{}.create()

	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Starts off in a non-leader state
	assert.Equal(t, false, h.IsLeader())
	assert.Nil(t, h.LeaderID())

	// Assign leadership via the rebalance listener and wait for the assignment to take effect
	cons.rebalanceEvents <- assignedPartitions(0, 1, 2)
	wait(t).UntilAsserted(isTrue(h.IsLeader))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Info)).
		Having(scribe.MessageEqual(fmt.Sprintf("Elected as leader, ID: %s", h.LeaderID()))).
		Passes(scribe.Count(1)))
	m.Reset()
	wait(t).UntilAsserted(func(t check.Tester) {
		if assert.Equal(t, 1, eh.length()) {
			e := eh.list()[0].(*LeaderElected)
			assert.Equal(t, e.LeaderID(), *(h.LeaderID()))
		}
	})

	// Revoke some partitions, but not containing leader partition (0)... check that still leader
	cons.rebalanceEvents <- revokedPartitions(1)
	wait(t).UntilAsserted(
		m.ContainsEntries().
			Having(scribe.LogLevel(scribe.Trace)).
			Having(scribe.MessageContaining("Revoked partitions")).
			Passes(scribe.Count(1)))
	m.Reset()
	assert.True(t, h.IsLeader())
	assert.Equal(t, 1, eh.length())

	// Revoke leadership via the rebalance listener and await its effect
	cons.rebalanceEvents <- revokedPartitions(0, 2)
	wait(t).UntilAsserted(isFalse(h.IsLeader))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Info)).
		Having(scribe.MessageEqual("Lost leader status")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Shutting down send battery")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Send battery terminated")).
		Passes(scribe.Count(1)))
	m.Reset()
	wait(t).UntilAsserted(func(t check.Tester) {
		if assert.Equal(t, 2, eh.length()) {
			_ = eh.list()[1].(*LeaderRevoked)
		}
	})

	// Assign a partition, but not the leader one... check that still not a leader
	cons.rebalanceEvents <- assignedPartitions(1)
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Trace)).
		Having(scribe.MessageContaining("Assigned partitions")).
		Passes(scribe.Count(1)))
	m.Reset()
	assert.False(t, h.IsLeader())
	assert.Equal(t, 2, eh.length())

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestMetrics(t *testing.T) {
	prodRef := concurrent.NewAtomicReference()
	m, _, cons, config := fixtures{producerMockSetup: func(prodMock *prodMock) {
		prodRef.Set(prodMock)
	}}.create()
	config.Limits.MinMetricsInterval = Duration(1 * time.Millisecond)

	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Induce leadership and wait for the leadership event
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).UntilAsserted(isNotNil(prodRef.Get))
	wait(t).UntilAsserted(func(t check.Tester) {
		assert.Equal(t, 1, eh.length())
	})

	wait(t).UntilAsserted(func(t check.Tester) {
		backlogRecords := generateRecords(1, 0)
		deliverAll(backlogRecords, nil, prodRef.Get().(*prodMock).events)
		if assert.GreaterOrEqual(t, eh.length(), 2) {
			e := eh.list()[1].(*MeterRead)
			if stats := e.Stats(); assert.NotNil(t, stats) {
				assert.Equal(t, stats.Name, "throughput")
			}
		}
	})
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageContaining("throughput")).
		Passes(scribe.CountAtLeast(1)))

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestHandleNonMessageEvent(t *testing.T) {
	prodRef := concurrent.NewAtomicReference()
	m, _, cons, config := fixtures{producerMockSetup: func(prodMock *prodMock) {
		prodRef.Set(prodMock)
	}}.create()
	config.Limits.MinMetricsInterval = Duration(1 * time.Millisecond)

	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Induce leadership and wait for the leadership event
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).UntilAsserted(isNotNil(prodRef.Get))
	prod := prodRef.Get().(*prodMock)
	wait(t).UntilAsserted(func(t check.Tester) {
		assert.Equal(t, 1, eh.length())
	})

	prod.events <- kafka.NewError(kafka.ErrAllBrokersDown, "brokers down", false)

	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Info)).
		Having(scribe.MessageContaining("Observed event: brokers down")).
		Passes(scribe.CountAtLeast(1)))

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestThrottleKeys(t *testing.T) {
	prod := concurrent.NewAtomicReference()
	lastPublished := concurrent.NewAtomicReference()
	m, db, cons, config := fixtures{producerMockSetup: func(pm *prodMock) {
		pm.f.Produce = func(m *prodMock, msg *kafka.Message, deliveryChan chan kafka.Event) error {
			lastPublished.Set(msg)
			return nil
		}
		prod.Set(pm)
	}}.create()

	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Starts off with no backlog.
	assert.Equal(t, 0, h.InFlightRecords())

	// Induce leadership and wait until a producer has been spawned.
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).UntilAsserted(isNotNil(prod.Get))

	const backlog = 10
	backlogRecords := generateCyclicKeyedRecords(1, backlog, 0)
	db.markedRecords <- backlogRecords

	// Even though we pushed several records through, they all had a common key, so only one should
	// should be published.
	wait(t).UntilAsserted(intEqual(1, h.InFlightRecords))
	assert.True(t, h.IsLeader()) // should definitely be leader by now
	wait(t).UntilAsserted(intEqual(1, prod.Get().(*prodMock).c.Produce.GetInt))
	msg := lastPublished.Get().(*kafka.Message)
	assert.Equal(t, msg.Value, []byte(backlogRecords[0].KafkaValue))
	assert.ElementsMatch(t, h.InFlightRecordKeys(), []string{backlogRecords[0].KafkaKey})

	// Drain the in-flight record... another one should then be published.
	deliverAll(backlogRecords[0:1], nil, prod.Get().(*prodMock).events)
	wait(t).UntilAsserted(func(t check.Tester) {
		msg := lastPublished.Get()
		if assert.NotNil(t, msg) {
			assert.Equal(t, msg.(*kafka.Message).Value, []byte(backlogRecords[1].KafkaValue))
		}
	})

	// Drain the backlog by feeding in delivery confirmations one at a time.
	for i := 1; i < backlog; i++ {
		wait(t).UntilAsserted(intEqual(1, h.InFlightRecords))
		wait(t).UntilAsserted(func(t check.Tester) {
			msg := lastPublished.Get()
			if assert.NotNil(t, msg) {
				assert.Equal(t, []byte(backlogRecords[i].KafkaValue), msg.(*kafka.Message).Value)
			}
		})
		deliverAll(backlogRecords[i:i+1], nil, prod.Get().(*prodMock).events)
	}

	// Revoke leadership...
	cons.rebalanceEvents <- revokedPartitions(0)

	// Wait for the backlog to drain... leadership status will be cleared when done.
	wait(t).Until(check.Not(h.IsLeader))

	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Shutting down send battery")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Send battery terminated")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Info)).
		Having(scribe.MessageContaining("Lost leader status")).
		Passes(scribe.Count(1)))

	assert.Equal(t, backlog, db.c.Purge.GetInt())
	assert.Equal(t, backlog, prod.Get().(*prodMock).c.Produce.GetInt())
	assert.Equal(t, 0, h.InFlightRecords())

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestPollDeadlineExceeded(t *testing.T) {
	m, db, cons, config := fixtures{}.create()

	config.Limits.DrainInterval = Duration(time.Millisecond)
	config.Limits.MaxPollInterval = Duration(time.Millisecond)
	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Starts off with no backlog.
	assert.Equal(t, 0, h.InFlightRecords())

	// Induce leadership and wait until a producer has been spawned.
	cons.rebalanceEvents <- assignedPartitions(0)

	db.markedRecords <- generateCyclicKeyedRecords(1, 2, 0)

	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Exceeded poll deadline")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Shutting down send battery")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Send battery terminated")).
		Passes(scribe.Count(1)))

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestQueueLimitExceeded(t *testing.T) {
	m, db, cons, config := fixtures{}.create()

	config.Limits.DrainInterval = Duration(time.Millisecond)
	config.Limits.QueueTimeout = Duration(time.Millisecond)
	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Starts off with no backlog.
	assert.Equal(t, 0, h.InFlightRecords())

	// Induce leadership and wait until a producer has been spawned.
	cons.rebalanceEvents <- assignedPartitions(0)

	db.markedRecords <- generateCyclicKeyedRecords(1, 2, 0)

	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Exceeded message queueing deadline")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Shutting down send battery")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Send battery terminated")).
		Passes(scribe.Count(1)))

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestDrainInFlightRecords_failedDelivery(t *testing.T) {
	prod := concurrent.NewAtomicReference()
	lastPublished := concurrent.NewAtomicReference()
	m, db, cons, config := fixtures{producerMockSetup: func(pm *prodMock) {
		pm.f.Produce = func(m *prodMock, msg *kafka.Message, deliveryChan chan kafka.Event) error {
			lastPublished.Set(msg)
			return nil
		}
		prod.Set(pm)
	}}.create()

	h, err := New(config)
	require.Nil(t, err)
	assertNoError(t, h.Start)

	// Starts off with no backlog
	assert.Equal(t, 0, h.InFlightRecords())

	// Induce leadership
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).UntilAsserted(isNotNil(prod.Get))

	// Generate a backlog
	const backlog = 10
	backlogRecords := generateRecords(backlog, 0)
	db.markedRecords <- backlogRecords

	// Wait for the backlog to register.
	wait(t).UntilAsserted(intEqual(backlog, h.InFlightRecords))
	wait(t).UntilAsserted(intEqual(backlog, prod.Get().(*prodMock).c.Produce.GetInt))
	assert.True(t, h.IsLeader()) // should be leader by now

	// Revoke leadership... this will start the backlog drain.
	cons.rebalanceEvents <- revokedPartitions(0)

	wait(t).Until(check.Not(h.IsLeader))

	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Shutting down send battery")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Debug)).
		Having(scribe.MessageEqual("Send battery terminated")).
		Passes(scribe.Count(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Info)).
		Having(scribe.MessageContaining("Lost leader status")).
		Passes(scribe.Count(1)))
	assert.Equal(t, h.InFlightRecords(), 0)

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestNonFatalErrorInReadMessage(t *testing.T) {
	m, _, cons, config := fixtures{}.create()

	cons.f.ReadMessage = func(m *consMock, timeout time.Duration) (*kafka.Message, error) {
		return nil, kafka.NewError(kafka.ErrAllBrokersDown, "simulated", false)
	}

	h, err := New(config)
	require.Nil(t, err)
	assertNoError(t, h.Start)

	// Wait for the error to be logged
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Error during poll")).
		Passes(scribe.CountAtLeast(1)))
	assert.Equal(t, Running, h.State())

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestNonFatalErrorInMarkQuery(t *testing.T) {
	m, db, cons, config := fixtures{}.create()

	db.f.Mark = func(m *dbMock, leaderID uuid.UUID) ([]OutboxRecord, error) {
		return nil, check.ErrSimulated
	}

	h, err := New(config)
	require.Nil(t, err)
	assertNoError(t, h.Start)

	// Induce leadership
	cons.rebalanceEvents <- assignedPartitions(0)

	// Wait for the error to be logged
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Error executing mark query")).
		Passes(scribe.CountAtLeast(1)))
	assert.Equal(t, Running, h.State())

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestErrorInProduce(t *testing.T) {
	prodRef := concurrent.NewAtomicReference()
	produceError := concurrent.NewAtomicCounter(1) // 1=true, 0=false
	m, db, cons, config := fixtures{producerMockSetup: func(pm *prodMock) {
		pm.f.Produce = func(m *prodMock, msg *kafka.Message, deliveryChan chan kafka.Event) error {
			if produceError.Get() == 1 {
				return kafka.NewError(kafka.ErrFail, "simulated", false)
			}
			return nil
		}
		prodRef.Set(pm)
	}}.create()

	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Induce leadership
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).UntilAsserted(isNotNil(prodRef.Get))
	prod := prodRef.Get().(*prodMock)
	prodRef.Set(nil)

	// Mark one record
	records := generateRecords(1, 0)
	db.markedRecords <- records

	// Wait for the error to be logged
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Error publishing record")).
		Passes(scribe.CountAtLeast(1)))
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Refreshed leader ID")).
		Passes(scribe.CountAtLeast(1)))
	m.Reset()
	assert.Equal(t, Running, h.State())
	wait(t).UntilAsserted(isNotNil(prodRef.Get))
	prod = prodRef.Get().(*prodMock)

	// Resume normal production... error should clear but the record count should not go up, as
	// there can only be one in-flight record for a given key
	produceError.Set(0)
	db.markedRecords <- records
	wait(t).UntilAsserted(intEqual(1, h.InFlightRecords))
	wait(t).UntilAsserted(func(t check.Tester) {
		assert.ElementsMatch(t, h.InFlightRecordKeys(), []string{records[0].KafkaKey})
	})

	if assert.GreaterOrEqual(t, eh.length(), 2) {
		_ = eh.list()[0].(*LeaderElected)
		_ = eh.list()[1].(*LeaderRefreshed)
	}

	// Feed successful delivery report for the first record
	prod.events <- message(records[0], nil)

	h.Stop()
	assert.Nil(t, h.Await())
}

// Tests remarking by feeding through two records for the same key, forcing them to come through in sequence.
// The first is published, but fails upon delivery, which raises the forceRemark flag.
// As the second on is processed, the forceRemark flag raised by the first should be spotted, and a leader
// refresh should occur.
func TestReset(t *testing.T) {
	prodRef := concurrent.NewAtomicReference()
	lastPublished := concurrent.NewAtomicReference()
	m, db, cons, config := fixtures{producerMockSetup: func(pm *prodMock) {
		pm.f.Produce = func(m *prodMock, msg *kafka.Message, deliveryChan chan kafka.Event) error {
			lastPublished.Set(msg)
			return nil
		}
		prodRef.Set(pm)
	}}.create()

	h, err := New(config)
	require.Nil(t, err)
	eh := &testEventHandler{}
	h.SetEventHandler(eh.handler())
	assertNoError(t, h.Start)

	// Induce leadership
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).UntilAsserted(isNotNil(prodRef.Get))
	prod := prodRef.Get().(*prodMock)

	// Mark two records for the same key
	records := generateCyclicKeyedRecords(1, 2, 0)
	db.markedRecords <- records

	// Wait for the backlog to register
	wait(t).UntilAsserted(intEqual(1, h.InFlightRecords))
	wait(t).UntilAsserted(func(t check.Tester) {
		if msg := lastPublished.Get(); assert.NotNil(t, msg) {
			assert.Equal(t, records[0].KafkaValue, string(msg.(*kafka.Message).Value))
		}
	})

	// Feed an error
	prod.events <- message(records[0], check.ErrSimulated)

	// Wait for the error to be logged
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Delivery failed")).
		Passes(scribe.CountAtLeast(1)))

	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Refreshed leader ID")).
		Passes(scribe.CountAtLeast(1)))
	m.Reset()
	assert.Equal(t, Running, h.State())
	wait(t).UntilAsserted(isNotNil(prodRef.Get))

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestErrorInPurgeAndResetQueries(t *testing.T) {
	prodRef := concurrent.NewAtomicReference()
	m, db, cons, config := fixtures{producerMockSetup: func(pm *prodMock) {
		prodRef.Set(pm)
	}}.create()

	records := generateRecords(2, 0)
	purgeError := concurrent.NewAtomicCounter(1) // 1=true, 0=false
	resetError := concurrent.NewAtomicCounter(1) // 1=true, 0=false
	db.f.Mark = func(m *dbMock, leaderID uuid.UUID) ([]OutboxRecord, error) {
		if db.c.Mark.Get() == 0 {
			return records, nil
		}
		return []OutboxRecord{}, nil
	}
	db.f.Purge = func(m *dbMock, id int64) (bool, error) {
		if purgeError.Get() == 1 {
			return false, check.ErrSimulated
		}
		return true, nil
	}
	db.f.Reset = func(m *dbMock, id int64) (bool, error) {
		if resetError.Get() == 1 {
			return false, check.ErrSimulated
		}
		return true, nil
	}

	h, err := New(config)
	require.Nil(t, err)
	assertNoError(t, h.Start)

	// Induce leadership and await its registration
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).UntilAsserted(isNotNil(prodRef.Get))
	prod := prodRef.Get().(*prodMock)

	wait(t).UntilAsserted(isTrue(h.IsLeader))
	wait(t).UntilAsserted(intEqual(2, h.InFlightRecords))

	// Feed successful delivery report for the first record
	prod.events <- message(records[0], nil)

	// Wait for the error to be logged
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Error executing purge query for record")).
		Passes(scribe.CountAtLeast(1)))
	m.Reset()
	assert.Equal(t, Running, h.State())
	assert.Equal(t, 2, h.InFlightRecords())

	// Resume normal production... error should clear
	purgeError.Set(0)
	wait(t).UntilAsserted(intEqual(1, h.InFlightRecords))

	// Feed failed delivery report for the first record
	prodRef.Get().(*prodMock).events <- message(records[1], kafka.NewError(kafka.ErrFail, "simulated", false))

	// Wait for the error to be logged
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Error executing reset query for record")).
		Passes(scribe.CountAtLeast(1)))
	m.Reset()
	assert.Equal(t, Running, h.State())
	assert.Equal(t, 1, h.InFlightRecords())

	// Resume normal production... error should clear
	resetError.Set(0)
	wait(t).UntilAsserted(intEqual(0, h.InFlightRecords))

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestIncompletePurgeAndResetQueries(t *testing.T) {
	prodRef := concurrent.NewAtomicReference()
	m, db, cons, config := fixtures{producerMockSetup: func(pm *prodMock) {
		prodRef.Set(pm)
	}}.create()

	records := generateRecords(2, 0)
	db.f.Mark = func(m *dbMock, leaderID uuid.UUID) ([]OutboxRecord, error) {
		if db.c.Mark.Get() == 0 {
			return records, nil
		}
		return []OutboxRecord{}, nil
	}
	db.f.Purge = func(m *dbMock, id int64) (bool, error) {
		return false, nil
	}
	db.f.Reset = func(m *dbMock, id int64) (bool, error) {
		return false, nil
	}

	h, err := New(config)
	require.Nil(t, err)
	assertNoError(t, h.Start)

	// Induce leadership and await its registration
	cons.rebalanceEvents <- assignedPartitions(0)
	wait(t).UntilAsserted(isTrue(h.IsLeader))
	wait(t).UntilAsserted(intEqual(2, h.InFlightRecords))
	wait(t).UntilAsserted(isNotNil(prodRef.Get))
	prod := prodRef.Get().(*prodMock)

	// Feed successful delivery report for the first record
	prod.events <- message(records[0], nil)

	// Wait for the warning to be logged
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Did not purge record")).
		Passes(scribe.CountAtLeast(1)))
	m.Reset()
	assert.Equal(t, Running, h.State())
	wait(t).UntilAsserted(intEqual(1, h.InFlightRecords))

	// Feed failed delivery report for the first record
	prod.events <- message(records[1], kafka.NewError(kafka.ErrFail, "simulated", false))

	// Wait for the warning to be logged
	wait(t).UntilAsserted(m.ContainsEntries().
		Having(scribe.LogLevel(scribe.Warn)).
		Having(scribe.MessageContaining("Did not reset record")).
		Passes(scribe.CountAtLeast(1)))
	m.Reset()
	assert.Equal(t, Running, h.State())
	wait(t).UntilAsserted(intEqual(0, h.InFlightRecords))

	h.Stop()
	assert.Nil(t, h.Await())
}

func TestEnsureState(t *testing.T) {
	check.ThatPanicsAsExpected(t, check.ErrorContaining("must not be false"), func() {
		ensureState(false, "must not be false")
	})

	ensureState(true, "must not be false")
}

func intEqual(expected int, intSupplier func() int) func(t check.Tester) {
	return func(t check.Tester) {
		assert.Equal(t, expected, intSupplier())
	}
}

func lengthEqual(expected int, sliceSupplier func() []string) func(t check.Tester) {
	return func(t check.Tester) {
		assert.Len(t, sliceSupplier(), expected)
	}
}

func atLeast(min int, f func() int) check.Assertion {
	return func(t check.Tester) {
		assert.GreaterOrEqual(t, f(), min)
	}
}

func isTrue(f func() bool) check.Assertion {
	return func(t check.Tester) {
		assert.True(t, f())
	}
}

func isFalse(f func() bool) check.Assertion {
	return func(t check.Tester) {
		assert.False(t, f())
	}
}

func isNotNil(f func() interface{}) check.Assertion {
	return func(t check.Tester) {
		assert.NotNil(t, f())
	}
}

func assertErrorContaining(t *testing.T, f func() error, substr string) {
	err := f()
	if assert.NotNil(t, err) {
		assert.Contains(t, err.Error(), substr)
	}
}

func assertNoError(t *testing.T, f func() error) {
	err := f()
	require.Nil(t, err)
}

func newTimedOutError() kafka.Error {
	return kafka.NewError(kafka.ErrTimedOut, "Timed out", false)
}

func generatePartitions(indexes ...int32) []kafka.TopicPartition {
	parts := make([]kafka.TopicPartition, len(indexes))
	for i, index := range indexes {
		parts[i] = kafka.TopicPartition{Partition: index}
	}
	return parts
}

func assignedPartitions(indexes ...int32) kafka.AssignedPartitions {
	return kafka.AssignedPartitions{Partitions: generatePartitions(indexes...)}
}

func revokedPartitions(indexes ...int32) kafka.RevokedPartitions {
	return kafka.RevokedPartitions{Partitions: generatePartitions(indexes...)}
}

func generateRecords(numRecords int, startID int) []OutboxRecord {
	records := make([]OutboxRecord, numRecords)
	now := time.Now()
	for i := 0; i < numRecords; i++ {
		records[i] = OutboxRecord{
			ID:         int64(startID + i),
			CreateTime: now,
			KafkaTopic: "test_topic",
			KafkaKey:   fmt.Sprintf("key-%x", i),
			KafkaValue: fmt.Sprintf("value-%x", i),
			KafkaHeaders: KafkaHeaders{
				KafkaHeader{Key: "ID", Value: strconv.FormatInt(int64(startID+i), 10)},
			},
		}
	}
	return records
}

func generateCyclicKeyedRecords(numKeys int, numRecords int, startID int) []OutboxRecord {
	records := make([]OutboxRecord, numRecords)
	now := time.Now()
	for i := 0; i < numRecords; i++ {
		records[i] = OutboxRecord{
			ID:         int64(startID + i),
			CreateTime: now,
			KafkaTopic: "test_topic",
			KafkaKey:   fmt.Sprintf("key-%x", i%numKeys),
			KafkaValue: fmt.Sprintf("value-%x", i),
			KafkaHeaders: KafkaHeaders{
				KafkaHeader{Key: "ID", Value: strconv.FormatInt(int64(startID+i), 10)},
			},
		}
	}
	return records
}

func message(record OutboxRecord, err error) *kafka.Message {
	return &kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &record.KafkaTopic, Error: err},
		Key:            []byte(record.KafkaKey),
		Value:          []byte(record.KafkaValue),
		Timestamp:      record.CreateTime,
		TimestampType:  kafka.TimestampCreateTime,
		Opaque:         record,
	}
}

func deliverAll(records []OutboxRecord, err error, events chan kafka.Event) {
	for _, record := range records {
		events <- message(record, err)
	}
}
*/
