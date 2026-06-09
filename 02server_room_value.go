package hgmRoomNotify

// 服务端向单条连接发送一次 roomValue. LiveData 能装进单个 frame 时发一条 Cmd_roomValue(行为同旧版);
// 超过 frame 上限时拆成 [Cmd_roomValueMore...][Cmd_roomValue] 多条, 由写缓冲整组原子写入,
// 保证同连接上这组分片连续到达、中间不插入其它消息, 客户端把累积的 More 分片拼到最后那条 roomValue 前面即可重组.
func (sconn *server_conn_t) sendRoomValue(roomEpoch string, changeSeq uint64, roomId string, cVersionId string, liveData []byte){
	// 单条 Cmd_roomValue 的序列化体积(不直接用 BinarySize, 因为它对 LiveData>65535 会直接报错, 而这里正是要据此判断是否分块).
	singleSize := 1 + (2 + len(roomId)) + (1 + len(roomEpoch)) + getUvarintOutputSize(changeSeq) + (1 + len(cVersionId)) + (2 + len(liveData))
	maxFrame := int(sconn.conn.raw.GetMaxFrameSize())
	if singleSize <= maxFrame {
		sconn.sendMsgNoBlock(Msg_t{
			Cmd:        Cmd_roomValue,
			RoomEpoch:  roomEpoch,
			ChangeSeq:  changeSeq,
			RoomId:     roomId,
			CVersionId: cVersionId,
			LiveData:   liveData,
		})
		return
	}
	msgs := buildRoomValueChunks(roomEpoch, changeSeq, roomId, cVersionId, liveData, maxFrame)
	sconn.handlePushResult(sconn.writeBuf.pushMsgsAtomic(msgs), roomId)
}

// 把一次大 LiveData 按 maxFrame 切成 roomValueMore...roomValue 序列.
// 分片大小取 maxFrame 减去末条 roomValue(携带全部元数据)的固定开销, 保证每条 More/roomValue 都不超过 maxFrame.
// 最后一片用普通 Cmd_roomValue 携带元数据(RoomId/RoomEpoch/ChangeSeq/CVersionId), 其余用 More 只带分片.
func buildRoomValueChunks(roomEpoch string, changeSeq uint64, roomId string, cVersionId string, liveData []byte, maxFrame int) []Msg_t {
	// 末条 roomValue 固定开销: 1(cmd) + (2+roomId) + (1+epoch) + uvarint(changeSeq) + (1+cVersionId) + 2(分片长度前缀).
	tailMeta := 1 + (2 + len(roomId)) + (1 + len(roomEpoch)) + getUvarintOutputSize(changeSeq) + (1 + len(cVersionId)) + 2
	chunkSize := maxFrame - tailMeta
	if chunkSize < 1 {
		chunkSize = 1
	}
	chunkCount := (len(liveData) + chunkSize - 1) / chunkSize
	msgs := make([]Msg_t, 0, chunkCount)
	for off := 0; off < len(liveData); off += chunkSize {
		end := off + chunkSize
		if end > len(liveData) {
			end = len(liveData)
		}
		chunk := liveData[off:end]
		if end >= len(liveData) {
			msgs = append(msgs, Msg_t{
				Cmd:        Cmd_roomValue,
				RoomEpoch:  roomEpoch,
				ChangeSeq:  changeSeq,
				RoomId:     roomId,
				CVersionId: cVersionId,
				LiveData:   chunk,
			})
		} else {
			msgs = append(msgs, Msg_t{
				Cmd:      Cmd_roomValueMore,
				LiveData: chunk,
			})
		}
	}
	return msgs
}
