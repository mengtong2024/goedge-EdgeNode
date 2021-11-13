// Copyright 2021 Liuxiangchao iwind.liu@gmail.com. All rights reserved.

package nodes

import (
	"github.com/TeaOSLab/EdgeCommon/pkg/nodeconfigs"
	teaconst "github.com/TeaOSLab/EdgeNode/internal/const"
	"github.com/TeaOSLab/EdgeNode/internal/events"
	"github.com/TeaOSLab/EdgeNode/internal/monitor"
	"github.com/iwind/TeaGo/maps"
	"net"
	"sync/atomic"
	"time"
)

// 发送监控流量
func init() {
	events.On(events.EventStart, func() {
		ticker := time.NewTicker(1 * time.Minute)
		go func() {
			for range ticker.C {
				// 加入到数据队列中
				if teaconst.InTrafficBytes > 0 {
					monitor.SharedValueQueue.Add(nodeconfigs.NodeValueItemTrafficIn, maps.Map{
						"total": teaconst.InTrafficBytes,
					})
				}
				if teaconst.OutTrafficBytes > 0 {
					monitor.SharedValueQueue.Add(nodeconfigs.NodeValueItemTrafficOut, maps.Map{
						"total": teaconst.OutTrafficBytes,
					})
				}

				// 重置数据
				atomic.StoreUint64(&teaconst.InTrafficBytes, 0)
				atomic.StoreUint64(&teaconst.OutTrafficBytes, 0)
			}
		}()
	})
}

// ClientConn 客户端连接
type ClientConn struct {
	rawConn  net.Conn
	isClosed bool
}

func NewClientConn(conn net.Conn, quickClose bool) net.Conn {
	if quickClose {
		tcpConn, ok := conn.(*net.TCPConn)
		if ok {
			// TODO 可以设置此值
			_ = tcpConn.SetLinger(3)
		}
	}

	return &ClientConn{rawConn: conn}
}

func (this *ClientConn) Read(b []byte) (n int, err error) {
	n, err = this.rawConn.Read(b)
	if n > 0 {
		atomic.AddUint64(&teaconst.InTrafficBytes, uint64(n))
	}
	return
}

func (this *ClientConn) Write(b []byte) (n int, err error) {
	n, err = this.rawConn.Write(b)
	if n > 0 {
		atomic.AddUint64(&teaconst.OutTrafficBytes, uint64(n))
	}
	return
}

func (this *ClientConn) Close() error {
	this.isClosed = true
	return this.rawConn.Close()
}

func (this *ClientConn) LocalAddr() net.Addr {
	return this.rawConn.LocalAddr()
}

func (this *ClientConn) RemoteAddr() net.Addr {
	return this.rawConn.RemoteAddr()
}

func (this *ClientConn) SetDeadline(t time.Time) error {
	return this.rawConn.SetDeadline(t)
}

func (this *ClientConn) SetReadDeadline(t time.Time) error {
	return this.rawConn.SetReadDeadline(t)
}

func (this *ClientConn) SetWriteDeadline(t time.Time) error {
	return this.rawConn.SetWriteDeadline(t)
}

func (this *ClientConn) IsClosed() bool {
	return this.isClosed
}
