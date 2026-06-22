package hgmRoomNotify

import (
	"bytes"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 校验 buildRoomValueChunks 的结构不变量: 末条是带全元数据的 Cmd_roomValue, 其余是只带分片的
// Cmd_roomValueMore, 每条序列化后都 <= maxFrame, 全部分片拼接后等于原始 liveData.
func assertRoomValueChunks(t *testing.T, msgs []Msg_t, maxFrame int, roomEpoch string, changeSeq uint64, roomId string, cVersionId string, liveData []byte) {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatalf("buildRoomValueChunks 返回空, liveData 长度=%d", len(liveData))
	}
	var reassembled []byte
	for i, m := range msgs {
		sz, errMsg := m.BinarySize()
		if errMsg != "" {
			t.Fatalf("第 %d 条 BinarySize 报错: %s", i, errMsg)
		}
		if sz > maxFrame {
			t.Fatalf("第 %d 条序列化体积 %d 超过 maxFrame %d", i, sz, maxFrame)
		}
		last := i == len(msgs)-1
		if last {
			if m.Cmd != Cmd_roomValue {
				t.Fatalf("末条 Cmd 应为 roomValue, 实际 %d", m.Cmd)
			}
			if m.RoomEpoch != roomEpoch || m.ChangeSeq != changeSeq || m.RoomId != roomId || m.CVersionId != cVersionId {
				t.Fatalf("末条元数据不对: epoch=%q seq=%d room=%q cver=%q", m.RoomEpoch, m.ChangeSeq, m.RoomId, m.CVersionId)
			}
		} else {
			if m.Cmd != Cmd_roomValueMore {
				t.Fatalf("第 %d 条(非末条) Cmd 应为 roomValueMore, 实际 %d", i, m.Cmd)
			}
		}
		reassembled = append(reassembled, m.LiveData...)
	}
	if !bytes.Equal(reassembled, liveData) {
		t.Fatalf("分片重组后与原始不一致: 重组长度=%d 原始长度=%d", len(reassembled), len(liveData))
	}
}

// 生成可校验内容(逐字节带位置特征), 用于发现分片错位/丢字节.
func makePayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + 7)
	}
	return b
}

// buildRoomValueChunks 纯函数回归: 小 maxFrame 下穷举各 liveData 长度, 覆盖正好等于分片边界 /
// 边界+1 / 整数倍等情况; 再用 cVersionId 占满 100 字节验证 tailMeta 把元数据开销算进了分片大小.
func TestBuildRoomValueChunks(t *testing.T) {
	// 小 maxFrame 穷举: 每个长度都跨越某个分片边界, 自然覆盖 ==chunkSize / +1 / 整数倍.
	for n := 1; n <= 400; n++ {
		liveData := makePayload(n)
		msgs := buildRoomValueChunks("epoch1", 42, "room:x", "cver-1", liveData, 64)
		assertRoomValueChunks(t, msgs, 64, "epoch1", 42, "room:x", "cver-1", liveData)
	}
	// cVersionId 占满 100 字节: 末条 roomValue 元数据开销变大, chunkSize 必须相应缩小,
	// 否则末条会超过 maxFrame. 这里专门盯住 tailMeta 的计算.
	bigCver := string(bytes.Repeat([]byte("c"), 100))
	for _, n := range []int{1, 999, 5000, 50000} {
		liveData := makePayload(n)
		msgs := buildRoomValueChunks("epoch-longer", 1<<20, "room:cver", bigCver, liveData, 1000)
		assertRoomValueChunks(t, msgs, 1000, "epoch-longer", 1<<20, "room:cver", bigCver, liveData)
	}
}

// 起一个进程内 server, 配置大 LiveData 上限. 返回 server / ws url / 清理函数.
func startChunkTestServer(t *testing.T, liveDataMaxSize int) (*ServerManager, string, func()) {
	t.Helper()
	srv := &ServerManager{
		WriteBufMaxBytes: 1024 * 1024,
		LiveDataMaxSize:  liveDataMaxSize,
	}
	mux := http.NewServeMux()
	mux.Handle("/ws", srv)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := &http.Server{Handler: mux}
	go func() { _ = httpSrv.Serve(ln) }()
	cleanup := func() {
		_ = httpSrv.Close()
		_ = ln.Close()
	}
	return srv, "ws://" + ln.Addr().String() + "/ws", cleanup
}

// 进房并等到收到首条(进房时服务端回的空 onChange), 保证服务端已把本连接登记进房间, 之后 FireChange 才会发到本连接.
func enterRoomAndWaitReady(t *testing.T, cli *Client, roomId string) (gotCh chan []byte, leaveFn func()) {
	t.Helper()
	gotCh = make(chan []byte, 16)
	leaveFn = cli.RoomEnter(roomId, func(ev *RoomOnChange_t) {
		b := make([]byte, len(ev.LiveData))
		copy(b, ev.LiveData)
		gotCh <- b
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if cli.IsConnectedSucc() && cli.GetRoomCount() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("客户端连接/进房超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-gotCh: // 丢弃进房首条空 onChange.
	case <-time.After(3 * time.Second):
		t.Fatal("等待进房首条 onChange 超时")
	}
	return gotCh, leaveFn
}

// 端到端: 200KB LiveData 经 roomValueMore...roomValue 分块 + 客户端重组后字节级一致.
// 守 pushMsgsAtomic 原子写入 + 客户端 roomValueReassembleBuf 重组这一整条路径.
func TestRoomValueChunk_RoundTrip(t *testing.T) {
	const roomId = "chunk:roundtrip"
	srv, wsUrl, cleanup := startChunkTestServer(t, 256*1024)
	defer cleanup()

	cli := &Client{ReadMsgMaxBytes: 256 * 1024}
	cli.SetWsDialUrl(wsUrl)
	defer cli.CloseForTest()

	gotCh, leaveFn := enterRoomAndWaitReady(t, cli, roomId)
	defer leaveFn()

	big := makePayload(200 * 1024)
	srv.FireChange(RoomEvent_t{RoomId: roomId, CVersionId: "v1", LiveData: big})

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case got := <-gotCh:
			if bytes.Equal(got, big) {
				return // 成功.
			}
			t.Fatalf("收到的 LiveData 与发送不一致: 收到长度=%d 期望=%d", len(got), len(big))
		case <-time.After(time.Until(deadline)):
			t.Fatal("等待分块 LiveData 到达超时")
		}
	}
}

// 跨端配置耦合(部署 footgun): 客户端 ReadMsgMaxBytes < 服务端 LiveDataMaxSize 时, 一条大 LiveData
// 分片累积会超过客户端重组上限, 客户端报 reassemble overflow 主动断开重连. 守的是这个真实耦合关系
// (注意: 不是"单条 ws message 过大", 单帧恒被分块封顶在 ~64KB).
func TestRoomValueChunk_ReadMsgMaxBytesTooSmall_Disconnect(t *testing.T) {
	const roomId = "chunk:overflow"
	srv, wsUrl, cleanup := startChunkTestServer(t, 256*1024)
	defer cleanup()

	// 客户端重组上限 64KB, 远小于服务端 256KB 的 LiveData 上限.
	overflowCh := make(chan string, 4)
	cli := &Client{
		ReadMsgMaxBytes: 64 * 1024,
		ObsFn: func(ev *ObsEvent_t) {
			if ev.Type == ObsEventType_clientConnClose && strings.Contains(ev.CloseDetail, "reassemble overflow") {
				select {
				case overflowCh <- ev.CloseDetail:
				default:
				}
			}
		},
	}
	cli.SetWsDialUrl(wsUrl)
	defer cli.CloseForTest()

	_, leaveFn := enterRoomAndWaitReady(t, cli, roomId)
	defer leaveFn()

	srv.FireChange(RoomEvent_t{RoomId: roomId, CVersionId: "v1", LiveData: makePayload(200 * 1024)})

	select {
	case <-overflowCh:
		return // 客户端确实因重组溢出主动断开.
	case <-time.After(5 * time.Second):
		t.Fatal("客户端未因 ReadMsgMaxBytes 过小而触发 reassemble overflow 断开")
	}
}
