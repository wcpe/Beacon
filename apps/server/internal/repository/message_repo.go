package repository

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// msgInsertBatchSize 是单条批量写的分批大小（上界保护，防单 SQL 参数触达驱动上限）。
const msgInsertBatchSize = 200

// MessageRepository 提供消息日表（msg_trace_YYYYMMDD / msg_payload_YYYYMMDD）的数据访问（FR-149/150）：
// 按 message_id 内嵌 UUIDv7 时间定当日表、跨日批自动拆分；元数据与 payload 同一事务写两表、message_id
// 冲突即忽略（消息终态一次性落库、无更新语义、重放幂等）。
type MessageRepository struct {
	db *gorm.DB
}

// NewMessageRepository 构造仓库。
func NewMessageRepository(db *gorm.DB) *MessageRepository {
	return &MessageRepository{db: db}
}

// FlushDaily 幂等批量把一批（可能跨日）终态消息记录落各自当日表，返回被去重（未落）的记录数。
//
// 流程：① 按 message_id 的 UUIDv7 内嵌时间 UTC 日分组（无法解析的计入去重、跳过）；
// ② 事务外按需建各当日 msg_trace 与 msg_payload 两张表（DDL 隐式提交，须在事务外）；
// ③ 一个事务内逐日先批插 trace（OnConflict{message_id} DoNothing）、再批插 payload（同冲突策略），
// 保证同一消息的元数据与 payload 同事务落库。任一日写失败整事务回滚，交写入通道重试（幂等）。
func (r *MessageRepository) FlushDaily(records []model.MessageRecord) (deduplicated int, err error) {
	if len(records) == 0 {
		return 0, nil
	}
	byDay := make(map[time.Time][]model.MessageRecord)
	for _, rec := range records {
		ms, ok := store.TimeMsFromUUIDv7(rec.Trace.MessageID)
		if !ok {
			deduplicated++
			continue
		}
		day := utcDayStart(ms)
		byDay[day] = append(byDay[day], rec)
	}
	// 事务外确保两类日表存在（DDL 隐式提交，不能置于事务内）。
	traceTableByDay := make(map[time.Time]string, len(byDay))
	payloadTableByDay := make(map[time.Time]string, len(byDay))
	for day := range byDay {
		traceName, e := store.EnsureDailyTable(r.db, &model.MsgTrace{}, day)
		if e != nil {
			return 0, e
		}
		payloadName, e := store.EnsureDailyTable(r.db, &model.MsgPayload{}, day)
		if e != nil {
			return 0, e
		}
		traceTableByDay[day] = traceName
		payloadTableByDay[day] = payloadName
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
		for day, recs := range byDay {
			traces, payloads := splitMessageRecords(recs)
			dup, insErr := insertMsgTraces(tx, traceTableByDay[day], traces)
			if insErr != nil {
				return insErr
			}
			deduplicated += dup
			if len(payloads) > 0 {
				if pErr := insertMsgPayloads(tx, payloadTableByDay[day], payloads); pErr != nil {
					return pErr
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deduplicated, nil
}

// splitMessageRecords 把合并记录拆为 trace 行与（存在的）payload 行两批。
func splitMessageRecords(recs []model.MessageRecord) (traces []model.MsgTrace, payloads []model.MsgPayload) {
	traces = make([]model.MsgTrace, 0, len(recs))
	payloads = make([]model.MsgPayload, 0, len(recs))
	for _, rec := range recs {
		traces = append(traces, rec.Trace)
		if rec.Payload != nil {
			payloads = append(payloads, *rec.Payload)
		}
	}
	return traces, payloads
}

// insertMsgTraces 幂等批插元数据行：message_id 主键冲突即忽略，返回被去重行数。
func insertMsgTraces(tx *gorm.DB, tableName string, rows []model.MsgTrace) (int, error) {
	res := tx.Table(tableName).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "message_id"}}, DoNothing: true}).
		CreateInBatches(&rows, msgInsertBatchSize)
	if res.Error != nil {
		return 0, res.Error
	}
	deduplicated := len(rows) - int(res.RowsAffected)
	if deduplicated < 0 {
		deduplicated = 0
	}
	return deduplicated, nil
}

// insertMsgPayloads 幂等批插 payload 行：message_id 主键冲突即忽略（与 trace 同事务）。
func insertMsgPayloads(tx *gorm.DB, tableName string, rows []model.MsgPayload) error {
	return tx.Table(tableName).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "message_id"}}, DoNothing: true}).
		CreateInBatches(&rows, msgInsertBatchSize).Error
}
