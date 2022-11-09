package goconcurrentqueue

import "context"

//先看这个代码, 这个比fifo_ queue简单一点.

// Fixed capacity FIFO (First In First Out) concurrent queue
type FixedFIFO struct {
	queue    chan interface{}
	lockChan chan struct{}
	// queue for watchers that will wait for next elements (if queue is empty at DequeueOrWaitForNextElement execution )
	waitForNextElementChan chan chan interface{}
}

func NewFixedFIFO(capacity int) *FixedFIFO {
	queue := &FixedFIFO{}
	queue.initialize(capacity)

	return queue
}
//capacity 传入queue的大小. 所以这个叫做fixed fifoqueue.大小初始化时候固定了
func (st *FixedFIFO) initialize(capacity int) {
	st.queue = make(chan interface{}, capacity)
	st.lockChan = make(chan struct{}, 1)
	st.waitForNextElementChan = make(chan chan interface{}, WaitForNextElementChanCapacity)
}

// Enqueue enqueues an element. Returns error if queue is locked or it is at full capacity.
func (st *FixedFIFO) Enqueue(value interface{}) error {
	if st.IsLocked() {
		return NewQueueError(QueueErrorCodeLockedQueue, "The queue is locked")
	}

	// check if there is a listener waiting for the next element (this element)
	select {
	case listener := <-st.waitForNextElementChan://首先看是否有消费者在等待.
// verify whether it is possible to notify the listener (it could be the listener is no longer
// available because the context expired:equeueOrWaitForNextElementContext)
		select {
		// sends the element through the listener's channel instead of enqueueing it //这里面消费者也叫listener.监听者.监听是否有物品新进入.
		case listener <- value://如果有消费者,那么我们把value直接给他即可.
		default:
			// push the element into the queue instead of sending it through the listener's channel (which is not available at this moment)
			return st.enqueueIntoQueue(value) //因为listener是并发的.他里面channel可以被其他的给弄上.所以我们每一次都套上select. 如果value无法给listner,那么value就放入queue里面.
		}

	default:
		// enqueue the element into the queue
		return st.enqueueIntoQueue(value)
	}

	return nil
}

// enqueueIntoQueue enqueues the given item directly into the regular queue
func (st *FixedFIFO) enqueueIntoQueue(value interface{}) error {
	select {
	case st.queue <- value:
	default:
		return NewQueueError(QueueErrorCodeFullCapacity, "FixedFIFO queue is at full capacity")
	}

	return nil
}

// Dequeue dequeues an element. Returns error if: queue is locked, queue is empty or internal channel is closed.
func (st *FixedFIFO) Dequeue() (interface{}, error) {
	if st.IsLocked() {
		return nil, NewQueueError(QueueErrorCodeLockedQueue, "The queue is locked")
	}

	select {
	case value, ok := <-st.queue:
		if ok {
			return value, nil
		}
		return nil, NewQueueError(QueueErrorCodeInternalChannelClosed, "internal channel is closed")
	default:
		return nil, NewQueueError(QueueErrorCodeEmptyQueue, "empty queue")
	}
}




//就是下面函数爆一层.
// DequeueOrWaitForNextElement dequeues an element (if exist) or waits until the next element gets enqueued and returns it.
// Multiple calls to DequeueOrWaitForNextElement() would enqueue multiple "listeners" for future enqueued elements.
func (st *FixedFIFO) DequeueOrWaitForNextElement() (interface{}, error) {
	return st.DequeueOrWaitForNextElementContext(context.Background())
}

// 是多生产者多消费者模型, 理解这个模型需要先理解消费者. 因为为空的时候他会进行阻塞.
// DequeueOrWaitForNextElementContext dequeues an element (if exist) or waits until the next element gets enqueued and returns it.
// Multiple calls to DequeueOrWaitForNextElementContext() would enqueue multiple "listeners" for future enqueued elements.
// When the passed context expires this function exits and returns the context' error// 这个函数是 队列弹出,但是需要等待下一个元素的到来.
func (st *FixedFIFO) DequeueOrWaitForNextElementContext(ctx context.Context) (interface{}, error) {
	if st.IsLocked() { //锁上我们就返回error
		return nil, NewQueueError(QueueErrorCodeLockedQueue, "The queue is locked")
	}

	select {
	case value, ok := <-st.queue: //我们按照逻辑来,首先我们dequeue元素时候,首先查询数据queue里面是否有东西.如果有,那么ok==True,我们直接返回value即可.
		if ok {
			return value, nil
		}
		return nil, NewQueueError(QueueErrorCodeInternalChannelClosed, "internal channel is closed")
	case <-ctx.Done(): //另外一种情况是上下文的时间到了.那me返回nil和error
		return nil, ctx.Err()
	// queue is empty, add a listener to wait until next enqueued element is ready
	default: //如果上面都不发生,就走default逻辑,也就是queue里面空了.这时候需要让消费者等待.
		// channel to wait for next enqueued element
		waitChan := make(chan interface{})

		select {
		// enqueue a watcher into the watchForNextElementChannel to wait for the next element//新建的waitchan我们尝试吧他放入waitForNextElementChan里面. 如果放入成功, //没有default语句时候: select 随机执行一个可运行的 case。如果没有 case 可运行，它将阻塞，直到有 case 可运行. 
		case st.waitForNextElementChan <- waitChan:
			// return the next enqueued element, if any
			select {
			case item := <-waitChan:
				return item, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			// too many watchers (waitForNextElementChanCapacity) enqueued waiting for next elements
			return nil, NewQueueError(QueueErrorCodeEmptyQueue, "empty queue and can't wait for next element")
		}

		//return nil, NewQueueError(QueueErrorCodeEmptyQueue, "empty queue")
	}
}








// GetLen returns queue's length (total enqueued elements)
func (st *FixedFIFO) GetLen() int {
	st.Lock()
	defer st.Unlock()

	return len(st.queue)
}

// GetCap returns the queue's capacity
func (st *FixedFIFO) GetCap() int {
	st.Lock()
	defer st.Unlock()

	return cap(st.queue)
}



//下面3个是锁控制函数. 锁通过channel来实现. 这种缓冲区为1的channel,本质就是锁. 因为你加锁之后只要不解锁就无法再加锁. 同理解锁也是,如果之后不加锁,再解锁也是会阻塞.
func (st *FixedFIFO) Lock() {
	// non-blocking fill the channel
	select {
	case st.lockChan <- struct{}{}:
	default:
	}
}

func (st *FixedFIFO) Unlock() {
	// non-blocking flush the channel
	select {
	case <-st.lockChan:
	default:
	}
}

func (st *FixedFIFO) IsLocked() bool {
	return len(st.lockChan) >= 1
}
