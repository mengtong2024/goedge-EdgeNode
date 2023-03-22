// Copyright 2022 Liuxiangchao iwind.liu@gmail.com. All rights reserved. Official site: https://goedge.cn .

package stats

import (
	"github.com/TeaOSLab/EdgeCommon/pkg/nodeconfigs"
	"github.com/TeaOSLab/EdgeCommon/pkg/rpc/pb"
	teaconst "github.com/TeaOSLab/EdgeNode/internal/const"
	"github.com/TeaOSLab/EdgeNode/internal/events"
	"github.com/TeaOSLab/EdgeNode/internal/goman"
	"github.com/TeaOSLab/EdgeNode/internal/remotelogs"
	"github.com/TeaOSLab/EdgeNode/internal/rpc"
	"github.com/iwind/TeaGo/logs"
	"github.com/iwind/TeaGo/types"
	timeutil "github.com/iwind/TeaGo/utils/time"
	"sync"
	"time"
)

var SharedBandwidthStatManager = NewBandwidthStatManager()

const bandwidthTimestampDelim = 2 // N秒平均，更为精确

func init() {
	if !teaconst.IsMain {
		return
	}

	events.On(events.EventLoaded, func() {
		goman.New(func() {
			SharedBandwidthStatManager.Start()
		})
	})
}

type BandwidthStat struct {
	Day      string
	TimeAt   string
	UserId   int64
	ServerId int64

	CurrentBytes     int64
	CurrentTimestamp int64
	MaxBytes         int64
	TotalBytes       int64

	CachedBytes         int64
	AttackBytes         int64
	CountRequests       int64
	CountCachedRequests int64
	CountAttackRequests int64
}

// BandwidthStatManager 服务带宽统计
type BandwidthStatManager struct {
	m map[string]*BandwidthStat // serverId@day@time => *BandwidthStat

	pbStats []*pb.ServerBandwidthStat

	lastTime string // 上一次执行的时间

	ticker *time.Ticker
	locker sync.Mutex
}

func NewBandwidthStatManager() *BandwidthStatManager {
	return &BandwidthStatManager{
		m:      map[string]*BandwidthStat{},
		ticker: time.NewTicker(1 * time.Minute), // 时间小于1分钟是为了更快速地上传结果
	}
}

func (this *BandwidthStatManager) Start() {
	for range this.ticker.C {
		err := this.Loop()
		if err != nil && !rpc.IsConnError(err) {
			remotelogs.Error("BANDWIDTH_STAT_MANAGER", err.Error())
		}
	}
}

func (this *BandwidthStatManager) Loop() error {
	var regionId int64
	nodeConfig, _ := nodeconfigs.SharedNodeConfig()
	if nodeConfig != nil {
		regionId = nodeConfig.RegionId
	}

	var now = time.Now()
	var day = timeutil.Format("Ymd", now)
	var currentTime = timeutil.FormatTime("Hi", now.Unix()/300*300) // 300s = 5 minutes

	if this.lastTime == currentTime {
		return nil
	}
	this.lastTime = currentTime

	var pbStats = []*pb.ServerBandwidthStat{}

	// 历史未提交记录
	if len(this.pbStats) > 0 {
		var expiredTime = timeutil.FormatTime("Hi", time.Now().Unix()-1200) // 只保留20分钟

		for _, stat := range this.pbStats {
			if stat.TimeAt > expiredTime {
				pbStats = append(pbStats, stat)
			}
		}
		this.pbStats = nil
	}

	this.locker.Lock()
	for key, stat := range this.m {
		if stat.Day < day || stat.TimeAt < currentTime {
			// 防止数据出现错误
			if stat.CachedBytes > stat.TotalBytes {
				stat.CachedBytes = stat.TotalBytes
			}
			if stat.AttackBytes > stat.TotalBytes {
				stat.AttackBytes = stat.TotalBytes
			}

			pbStats = append(pbStats, &pb.ServerBandwidthStat{
				Id:                  0,
				UserId:              stat.UserId,
				ServerId:            stat.ServerId,
				Day:                 stat.Day,
				TimeAt:              stat.TimeAt,
				Bytes:               stat.MaxBytes / bandwidthTimestampDelim,
				TotalBytes:          stat.TotalBytes,
				CachedBytes:         stat.CachedBytes,
				AttackBytes:         stat.AttackBytes,
				CountRequests:       stat.CountRequests,
				CountCachedRequests: stat.CountCachedRequests,
				CountAttackRequests: stat.CountAttackRequests,
				NodeRegionId:        regionId,
			})
			delete(this.m, key)
		}
	}
	this.locker.Unlock()

	if len(pbStats) > 0 {
		// 上传
		rpcClient, err := rpc.SharedRPC()
		if err != nil {
			return err
		}
		_, err = rpcClient.ServerBandwidthStatRPC.UploadServerBandwidthStats(rpcClient.Context(), &pb.UploadServerBandwidthStatsRequest{ServerBandwidthStats: pbStats})
		if err != nil {
			this.pbStats = pbStats

			return err
		}
	}

	return nil
}

// AddBandwidth 添加带宽数据
func (this *BandwidthStatManager) AddBandwidth(userId int64, serverId int64, peekBytes int64, totalBytes int64) {
	if serverId <= 0 || (peekBytes == 0 && totalBytes == 0) {
		return
	}

	var now = time.Now()
	var timestamp = now.Unix() / bandwidthTimestampDelim * bandwidthTimestampDelim // 将时间戳均分成N等份
	var day = timeutil.Format("Ymd", now)
	var timeAt = timeutil.FormatTime("Hi", now.Unix()/300*300)
	var key = types.String(serverId) + "@" + day + "@" + timeAt

	// 增加TCP Header尺寸，这里默认MTU为1500，且默认为IPv4
	const mtu = 1500
	const tcpHeaderSize = 20
	if peekBytes > mtu {
		peekBytes += peekBytes * tcpHeaderSize / mtu
	}

	this.locker.Lock()
	stat, ok := this.m[key]
	if ok {
		// 此刻如果发生用户ID（userId）的变化也忽略，等N分钟后有新记录后再换

		if stat.CurrentTimestamp == timestamp {
			stat.CurrentBytes += peekBytes
		} else {
			stat.CurrentBytes = peekBytes
			stat.CurrentTimestamp = timestamp
		}
		if stat.CurrentBytes > stat.MaxBytes {
			stat.MaxBytes = stat.CurrentBytes
		}

		stat.TotalBytes += totalBytes
	} else {
		this.m[key] = &BandwidthStat{
			Day:              day,
			TimeAt:           timeAt,
			UserId:           userId,
			ServerId:         serverId,
			CurrentBytes:     peekBytes,
			MaxBytes:         peekBytes,
			TotalBytes:       totalBytes,
			CurrentTimestamp: timestamp,
		}
	}
	this.locker.Unlock()
}

// AddTraffic 添加请求数据
func (this *BandwidthStatManager) AddTraffic(serverId int64, cachedBytes int64, countRequests int64, countCachedRequests int64, countAttacks int64, attackBytes int64) {
	var now = time.Now()
	var day = timeutil.Format("Ymd", now)
	var timeAt = timeutil.FormatTime("Hi", now.Unix()/300*300)
	var key = types.String(serverId) + "@" + day + "@" + timeAt
	this.locker.Lock()
	// 只有有记录了才会添加
	stat, ok := this.m[key]
	if ok {
		stat.CachedBytes += cachedBytes
		stat.CountRequests += countRequests
		stat.CountCachedRequests += countCachedRequests
		stat.CountAttackRequests += countAttacks
		stat.AttackBytes += attackBytes
	}
	this.locker.Unlock()
}

func (this *BandwidthStatManager) Inspect() {
	this.locker.Lock()
	logs.PrintAsJSON(this.m)
	this.locker.Unlock()
}

func (this *BandwidthStatManager) Map() map[int64]int64 /** serverId => max bytes **/ {
	this.locker.Lock()
	defer this.locker.Unlock()

	var m = map[int64]int64{}
	for _, v := range this.m {
		m[v.ServerId] = v.MaxBytes / bandwidthTimestampDelim
	}

	return m
}
