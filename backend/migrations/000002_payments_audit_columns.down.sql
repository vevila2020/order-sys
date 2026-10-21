-- 回滚 000002：删除新增的两列。
--
-- 仅在开发与演练中使用。一旦这两列承载了真实数据（失败原因、迁移次数），
-- 回滚就是数据丢失，生产环境应走备份恢复而不是执行本文件。

ALTER TABLE payments
    DROP COLUMN failure_reason,
    DROP COLUMN version;
