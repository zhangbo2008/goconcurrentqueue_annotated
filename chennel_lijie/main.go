package main

import (
	"fmt"
)

func main() {
	ch := make(chan int, 1) //如果把1去掉,会死锁. 因为无缓冲区的话,需要2个进程都开始运行,数据才会流动.// 所以结论是  综上所述：不能将无缓存通道理解为缓冲通道为1的通道！
	ch <- 1
	fmt.Println(<-ch)
}
