-- 更新黑名单条目（管理面：title/block_type/expires_at，updated_at 顺带刷新）。
-- ★ warn_times 不在管理面编辑范围：显式写 warn_times = warn_times 保留原值（封禁次数仅
--   经封禁入库/恢复续封语义变动，见 ip_blacklist_restore_ban.sql）。
-- 参数：?1=title ?2=block_type ?3=expires_at（可空） ?4=updated_at(UTC) ?5=id
UPDATE {table}
SET title = ?, block_type = ?, expires_at = ?, warn_times = warn_times, updated_at = ?
WHERE id = ?
