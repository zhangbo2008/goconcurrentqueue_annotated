package goconcurrentqueue

// fifoqueue结构体,核心代码全在这一个文件里面.
import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	WaitForNextElementChanCapacity           = 1000
	dequeueOrWaitForNextElementInvokeGapTime = 10
)

// FIFO (First In First Out) concurrent queue  // 整个queue, 有一个容量, 塞满了就进行阻塞.
type FIFO struct {
	slice       []interface{} //首先存底层数据用切片
	rwmutex     sync.RWMutex
	lockRWmutex sync.RWMutex
	isLocked    bool
	// queue for watchers that will wait for next elements (if queue is empty at DequeueOrWaitForNextElement execution )
	waitForNextElementChan chan chan interface{}
	// queue to unlock consumers that were locked when queue was empty (during DequeueOrWaitForNextElement execution)
	unlockDequeueOrWaitForNextElementChan chan struct{}
}

// NewFIFO returns a new FIFO concurrent queue
func NewFIFO() *FIFO {
	ret := &FIFO{}
	ret.initialize()

	return ret
}

func (st *FIFO) initialize() {
	st.slice = make([]interface{}, 0)                                                       //注意这里初始化是切片.切片是自动扩容的.所以这个queue容量是无限的.
	st.waitForNextElementChan = make(chan chan interface{}, WaitForNextElementChanCapacity) // 这2个channel的容量都是1000,2个都是双向channel
	st.unlockDequeueOrWaitForNextElementChan = make(chan struct{}, WaitForNextElementChanCapacity)
}

// 往队列里面加入一个元素.
// Enqueue enqueues an element. Returns error if queue is locked.
func (st *FIFO) Enqueue(value interface{}) error {
	if st.isLocked {
		return NewQueueError(QueueErrorCodeLockedQueue, "The queue is locked")
	}

	// let consumers (DequeueOrWaitForNextElement) know there is a new element
	select {
	case st.unlockDequeueOrWaitForNextElementChan <- struct{}{}: // 新加一个元素的时候我们就触发一个信号.告诉消费者可以dequeue了.
	default:
		// message could not be sent   //当 select 中的其他条件分支都没有准备好的时候，`default` 分支会被执行。
	}

	// check if there is a listener waiting for the next element (this element)
	select {
	case listener := <-st.waitForNextElementChan:
		// send the element through the listener's channel instead of enqueue it
		select {
		case listener <- value: //有坚挺着,就直接把数据给他!!!!!!!!!!!这个地方要注意为什么上面一层case触发了,这里面还有啊进行一个case来触发呢???????下面的说法是listener虽然能取出来,但是他有可能还没ready,因为他也是一个channel,有可能无法往里面放入数据!这透露出来一个go的书写逻辑就是只要涉及channel的读写我们就要使用select函数!!!!!!!!!!!!!!具体原因看127行.
		default:
			// enqueue if listener is not ready

			// lock the object to enqueue the element into the slice
			st.rwmutex.Lock()
			// enqueue the element
			st.slice = append(st.slice, value) //没有坚挺着,就直接把数据放slice里面存着.
			defer st.rwmutex.Unlock()
		}

	default:
		// lock the object to enqueue the element into the slice
		st.rwmutex.Lock()
		// enqueue the element
		st.slice = append(st.slice, value)
		defer st.rwmutex.Unlock()
	}

	return nil
}

// Dequeue dequeues an element. Returns error if queue is locked or empty.
func (st *FIFO) Dequeue() (interface{}, error) {
	if st.isLocked {
		return nil, NewQueueError(QueueErrorCodeLockedQueue, "The queue is locked")
	}

	st.rwmutex.Lock()
	defer st.rwmutex.Unlock()

	len := len(st.slice)
	if len == 0 {
		return nil, NewQueueError(QueueErrorCodeEmptyQueue, "empty queue")
	}

	elementToReturn := st.slice[0]
	st.slice = st.slice[1:]

	return elementToReturn, nil
}

// DequeueOrWaitForNextElement dequeues an element (if exist) or waits until the next element gets enqueued and returns it.
// Multiple calls to DequeueOrWaitForNextElement() would enqueue multiple "listeners" for future enqueued elements.
func (st *FIFO) DequeueOrWaitForNextElement() (interface{}, error) {
	return st.DequeueOrWaitForNextElementContext(context.Background())
}

// 还是要从消费者这个核心代码开始理解!!!!!!!!!!!!!!
// DequeueOrWaitForNextElementContext dequeues an element (if exist) or waits until the next element gets enqueued and returns it.
// Multiple calls to DequeueOrWaitForNextElementContext() would enqueue multiple "listeners" for future enqueued elements.
// When the passed context expires this function exits and returns the context' error
func (st *FIFO) DequeueOrWaitForNextElementContext(ctx context.Context) (interface{}, error) {
	for { //一直死循环.
		if st.isLocked {
			return nil, NewQueueError(QueueErrorCodeLockedQueue, "The queue is locked")
		}

		// get the slice's len
		st.rwmutex.Lock()
		length := len(st.slice)
		st.rwmutex.Unlock()

		//首先我们看queue已经为空了.这时候情况复杂.我们要让消费者等待.
		if length == 0 {
			// channel to wait for next enqueued element
			waitChan := make(chan interface{}) // 注意这个地方是没有缓冲区的channel,所以他完全有可能在读写时候发生阻塞.142行发生消费时候的同时,必须有输入进入管道管道才会流通,否则会阻塞.

			select {
			// enqueue a watcher into the watchForNextElementChannel to wait for the next element
			case st.waitForNextElementChan <- waitChan: // 把waitchannel放入等待chan里面.

				// n
				for {
					// re-checks every i milliseconds (top: 10 times) ... the following verifies if an item was enqueued
					// around the same time DequeueOrWaitForNextElementContext was invoked, meaning the waitChan wasn't yet sent over
					// st.waitForNextElementChan   //首先遍历10遍,在waitchan里面读取数据,如果读到了数据就return了.
					for i := 0; i < dequeueOrWaitForNextElementInvokeGapTime; i++ {
						select {
						case <-ctx.Done():
							return nil, ctx.Err()
						case dequeuedItem := <-waitChan: //或者waitchan
							return dequeuedItem, nil
						case <-time.After(time.Millisecond * time.Duration(i)): // 10微秒之后我们开始从queue里面取数据.因为过了这些事件有可能数据进去queue
							if dequeuedItem, err := st.Dequeue(); err == nil { //从queue里面取.
								return dequeuedItem, nil
							} //时间超时并且到10次了就退出循环.
						}
					}
					//如果上面的没return到东西,就说明有可能数据刚enqueue,这时候我们查看unlockDequeueOrWaitForNextElementChan即可.他是enqueue时候触发的信号.
					// return the next enqueued element, if any
					select {
					// new enqueued element, no need to keep waiting
					case <-st.unlockDequeueOrWaitForNextElementChan:
						// check if we got a new element just after we got <-st.unlockDequeueOrWaitForNextElementChan
						select { //收到信号之后我们就查询一下是否有新物品在waitchan里面.
						case item := <-waitChan:
							return item, nil
						default:
						}
						// go back to: for loop
						continue

					case item := <-waitChan:
						return item, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					// n
				}
			default:
				// too many watchers (waitForNextElementChanCapacity) enqueued waiting for next elements
				return nil, NewQueueError(QueueErrorCodeEmptyQueue, "empty queue and can't wait for next element because there are too many DequeueOrWaitForNextElement() waiting")
			}
		}
		//下面逻辑会在queue不空时候触发.返回queue里面元素即可.
		st.rwmutex.Lock()

		// verify that at least 1 item resides on the queue
		if len(st.slice) == 0 {
			st.rwmutex.Unlock()
			continue
		}
		elementToReturn := st.slice[0]
		st.slice = st.slice[1:]

		st.rwmutex.Unlock()
		return elementToReturn, nil
	}
}

// Get returns an element's value and keeps the element at the queue
func (st *FIFO) Get(index int) (interface{}, error) {
	if st.isLocked {
		return nil, NewQueueError(QueueErrorCodeLockedQueue, "The queue is locked")
	}

	st.rwmutex.RLock()
	defer st.rwmutex.RUnlock()

	if len(st.slice) <= index {
		return nil, NewQueueError(QueueErrorCodeIndexOutOfBounds, fmt.Sprintf("index out of bounds: %v", index))
	}

	return st.slice[index], nil
}

// Remove removes an element from the queue
func (st *FIFO) Remove(index int) error {
	if st.isLocked {
		return NewQueueError(QueueErrorCodeLockedQueue, "The queue is locked")
	}

	st.rwmutex.Lock()
	defer st.rwmutex.Unlock()

	if len(st.slice) <= index {
		return NewQueueError(QueueErrorCodeIndexOutOfBounds, fmt.Sprintf("index out of bounds: %v", index))
	}

	// remove the element
	st.slice = append(st.slice[:index], st.slice[index+1:]...)

	return nil
}

// GetLen returns the number of enqueued elements
func (st *FIFO) GetLen() int {
	st.rwmutex.RLock()
	defer st.rwmutex.RUnlock()

	return len(st.slice)
}

// GetCap returns the queue's capacity
func (st *FIFO) GetCap() int {
	st.rwmutex.RLock()
	defer st.rwmutex.RUnlock()

	return cap(st.slice)
}

// Lock // Locks the queue. No enqueue/dequeue operations will be allowed after this point.
func (st *FIFO) Lock() {
	st.lockRWmutex.Lock()
	defer st.lockRWmutex.Unlock()

	st.isLocked = true
}

// Unlock unlocks the queue
func (st *FIFO) Unlock() {
	st.lockRWmutex.Lock()
	defer st.lockRWmutex.Unlock()

	st.isLocked = false
}

// IsLocked returns true whether the queue is locked
func (st *FIFO) IsLocked() bool {
	st.lockRWmutex.RLock()
	defer st.lockRWmutex.RUnlock()

	return st.isLocked
}
