#!/usr/bin/env bash
# release-body.sh - 生成 GitHub Release 正文（自上一 tag 以来的提交，按前缀分组 + 统计概要）。
#
# 用法:
#   scripts/release-body.sh [TAG]     # TAG 缺省取最近 tag；CI 传 $GITHUB_REF_NAME
#
# 输出: Markdown 写 stdout。CI 与本地预览共用同一脚本（单一事实源，所见即所得）：
#   本地: scripts/release-body.sh v0.3.0
#   CI:   body="$(scripts/release-body.sh "$GITHUB_REF_NAME")"
#
# 分组规则: feat/fix/docs/chore 精确前缀优先（可叠加 批次N- 序号）；
# 历史无前缀提交按关键词启发式兜底，未识别归 chore。
# 提交前缀规范见 AGENTS.md「Commit & Pull Request Guidelines」。
set -u

# ---------- 参数与 tag 解析 ----------
TAG="${1:-}"
if [ -z "$TAG" ]; then
  TAG="$(git describe --tags --abbrev=0 2>/dev/null || true)"
fi
if [ -z "$TAG" ] || ! git rev-parse -q --verify "$TAG" >/dev/null 2>&1; then
  echo "错误: 无法解析 tag（用法: scripts/release-body.sh <tag>）" >&2
  exit 1
fi

# ---------- 上一 tag（无则列全量提交） ----------
PREV=""
if git rev-parse -q --verify "$TAG^" >/dev/null 2>&1; then
  PREV="$(git describe --tags --abbrev=0 "$TAG^" 2>/dev/null || true)"
fi
if [ -n "$PREV" ] && [ "$PREV" != "$TAG" ]; then
  LOG="$(git log --oneline --no-merges "$PREV..$TAG")"
else
  LOG="$(git log --oneline --no-merges "$TAG")"
fi

# ---------- 提交分类 ----------
classify() {
  case "$1" in
    批次[0-9]*-feat:*|feat:*) echo feat ;;
    批次[0-9]*-fix:*|fix:*) echo fix ;;
    批次[0-9]*-docs:*|docs:*) echo docs ;;
    批次[0-9]*-chore:*|chore:*) echo chore ;;
    *修复*|*修正*|*误判*|*回退*) echo fix ;;
    *文档*|*README*|*注释*|*说明*) echo docs ;;
    *新增*|*支持*|*落地*|*实现*|*增强*|*升级*|*组件化*|*改造*|*打包*|*发布*|*CI*|*CD*) echo feat ;;
    *) echo chore ;;
  esac
}

# 去掉分类前缀（批次N- 与 feat:/fix:/docs:/chore:），保留中文描述
strip_prefix() {
  printf '%s' "$1" | sed -E 's/^(批次[0-9]+-)?(feat|fix|docs|chore):[[:space:]]*//'
}

# 摘要截断（按字符，超长补省略号；用 bash 内建扩展，避免 coreutils 按字节切半中文）
truncate_summary() {
  local s="$1"
  if [ "${#s}" -gt 100 ]; then
    printf '%s…' "${s:0:100}"
  else
    printf '%s' "$s"
  fi
}

# ---------- commit 链接仓库（无 origin 时退化为短 hash） ----------
REPO=""
REMOTE="$(git config --get remote.origin.url 2>/dev/null || true)"
case "$REMOTE" in
  git@github.com:*) REPO="${REMOTE#git@github.com:}"; REPO="${REPO%.git}" ;;
  https://github.com/*) REPO="${REMOTE#https://github.com/}"; REPO="${REPO%.git}" ;;
  github:*) REPO="${REMOTE#github:}"; REPO="${REPO%.git}" ;;
esac

# ---------- 逐条解析 ----------
feat=(); fix=(); docs=(); chore=()
while IFS= read -r line; do
  [ -z "$line" ] && continue
  hash="${line%% *}"
  raw="${line#* }"
  group="$(classify "$raw")"
  summary="$(truncate_summary "$(strip_prefix "$raw")")"
  if [ -n "$REPO" ]; then
    item="- ${summary} ([${hash:0:7}](https://github.com/${REPO}/commit/${hash}))"
  else
    item="- ${summary} (${hash:0:7})"
  fi
  case "$group" in
    feat) feat+=("$item") ;;
    fix) fix+=("$item") ;;
    docs) docs+=("$item") ;;
    *) chore+=("$item") ;;
  esac
done <<< "$LOG"

total=$(( ${#feat[@]} + ${#fix[@]} + ${#docs[@]} + ${#chore[@]} ))

# ---------- 输出正文 ----------
echo "## ${TAG} 变更概要"
echo ""
if [ "$total" -eq 0 ]; then
  echo "本次发布无提交变更。"
  exit 0
fi
printf '本次共 %d 个提交：feat %d、fix %d、docs %d、chore %d。\n' "$total" "${#feat[@]}" "${#fix[@]}" "${#docs[@]}" "${#chore[@]}"

emit_group() {
  local title="$1"; shift
  [ "$#" -eq 0 ] && return
  echo ""
  if [ "$#" -gt 15 ]; then
    echo "<details>"
    echo "<summary>${title}（$# 条）</summary>"
    echo ""
    for item in "$@"; do printf '%s\n' "$item"; done
    echo ""
    echo "</details>"
  else
    echo "### ${title}"
    echo ""
    for item in "$@"; do printf '%s\n' "$item"; done
  fi
}

emit_group "✨ feat 新功能" "${feat[@]}"
emit_group "🐛 fix 修复" "${fix[@]}"
emit_group "📝 docs 文档" "${docs[@]}"
emit_group "🔧 chore 杂项" "${chore[@]}"
echo ""
