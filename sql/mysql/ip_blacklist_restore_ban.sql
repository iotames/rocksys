-- 封禁恢复续封（决策 8/10：软删/过期条目拉回小黑屋——清 deleted_at + 重设 expires_at + warn_times 覆写）。
-- 与 ip_blacklist_restore.sql（管理面纯恢复，仅清 deleted_at）语义不同，互不影响。
-- 满限转永久由 Go 侧判定后传参：expires_at=NULL + title 追加转永久标记（见 IPListStore.RestoreBan）。
-- 参数：?1=expires_at（可空，NULL=永久） ?2=warn_times（累加后的新值） ?3=title ?4=updated_at(UTC) ?5=id
UPDATE {table}
SET deleted_at = NULL, expires_at = ?, warn_times = ?, title = ?, updated_at = ?
WHERE id = ?
