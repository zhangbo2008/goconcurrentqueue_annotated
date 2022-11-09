package main

//通过这个例子可以看出来context是控制go线程的生命周期的. 利用的就是Done方法.
import (
	"context"
	"fmt"
	"time"
)

func watch1(ctx context.Context, name string) {
	for {
		select {
		case <-ctx.Done():
			fmt.Println(name, "收到信号，监控退出，time=", time.Now().Unix())
			return
		default:
			fmt.Println(name, "gouroutine监控中,time=", time.Now().Unix())
			time.Sleep(1 * time.Second)
		}

	}
}

func main() {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second*3))

	go watch1(ctx, "监控1")
	go watch1(ctx, "监控2")

	fmt.Println("现在开始等待5秒,time=", time.Now().Unix())
	time.Sleep(5 * time.Second)
	fmt.Println("等待5秒结束,准备调用cancel()函数，发现两个子协程已经结束了，time=", time.Now().Unix())
	cancel()

}
