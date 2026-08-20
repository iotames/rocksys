#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""分析 nginx JSON 日志中的爬虫/攻击流量（v2：合并多文件统计）。

相比 v1 的核心改进：
  1. 合并多个日志文件流式统计（不再写死单文件）；
  2. 新增【完整 UA 指纹 Top N】：按原始 UA 文本分组计数，未命中已知爬虫的
     特殊指纹会被自动判定并输出候选清单（供审核后追加到 crawler_ua.txt）；
  3. 新增【风险 IP 表】：聚合每个 IP 的异常状态码 / 攻击路径探测 / 特殊 UA /
     陌生 Host 探测 请求数，按加权风险分排序，输出候选 IP 黑名单；
  4. URI Top 改为【异常探测 URI】：过滤静态资源与 200 成功请求，聚焦攻击探测路径。

两遍流式扫描：第一遍学 Host 分布（判定"陌生 Host"），第二遍聚合全部维度。
内存占用与文件大小无关（只保留有限集合的计数），适合百 MB 级日志。

输出：
  - 控制台报告（分节 1-6）
  - report_ua_fingerprint_candidates.txt  特殊 UA 指纹候选（追加 crawler_ua.txt 用）
  - report_risk_ips.txt                   风险 IP 候选（追加 IP 黑名单用）
"""
import json
import re
import sys
import os
from collections import Counter, defaultdict

# ── 配置 ──────────────────────────────────────────────────────────────

# 合并统计的日志文件（相对脚本所在目录）
LOG_FILES = [
    "nginx.blog_yoursite_com.json.log",
    "nginx.blog_yoursite_com-1.json.log",
]

# 日志格式
# {"hello_arg": "", "remote_addr": "114.119.xxx.xx", "request_uri": "/archives/289.html", "get_querys_args": "", "request_length": "466", "request_time": "0.041", "request_method": "GET", "status": "200", "body_bytes_sent": "17860", "http_referer": "https://www.yoursite.com/category/default/13/", "http_user_agent": "Mozilla/5.0 (Linux; Android 7.0;) AppleWebKit/537.36 (HTML, like Gecko) Mobile Safari/537.36 (compatible; PetalBot;+https://webmaster.petalsearch.com/site/petalbot)", "http_x_forwarded_for": "", "http_host": "www.yoursite.com", "server_name": "blog.yoursite.com", "upstream": "172.18.0.5:9000", "upstream_response_time": "0.040", "upstream_status": "200"}


# 已知爬虫 UA 特征（仅作"已知/未知"标注，不再作为过滤条件；长特征在前）
KNOWN_UA_PATTERNS = [
    ("ahrefs",        re.compile(r"ahrefsbot", re.I)),
    ("semrush",       re.compile(r"semrushbot", re.I)),
    ("mj12",          re.compile(r"mj12bot", re.I)),
    ("bingbot",       re.compile(r"bingbot", re.I)),
    ("googlebot",     re.compile(r"googlebot", re.I)),
    ("baiduspider",   re.compile(r"baiduspider", re.I)),
    ("360Spider",     re.compile(r"360spider", re.I)),
    ("Sogou",         re.compile(r"sogou", re.I)),
    ("Yandex",        re.compile(r"yandex", re.I)),
    ("DotBot",        re.compile(r"dotbot", re.I)),
    ("PetalBot",      re.compile(r"petalbot", re.I)),
    ("Bytespider",    re.compile(r"bytespider", re.I)),
    ("GPTBot",        re.compile(r"gptbot", re.I)),
    ("CCBot",         re.compile(r"ccbot", re.I)),
    ("ClaudeBot",     re.compile(r"claudebot", re.I)),
    ("Amazonbot",     re.compile(r"amazonbot", re.I)),
    ("facebook",      re.compile(r"facebookexternalhit", re.I)),
    ("DataForSeo",    re.compile(r"dataforseobot", re.I)),
    ("BLEXBot",       re.compile(r"blexbot", re.I)),
    ("SeznamBot",     re.compile(r"seznambot", re.I)),
    ("archive/crawler", re.compile(r"nutch|archive\.org|crawler", re.I)),
    ("脚本爬虫",       re.compile(r"scrapy|python-requests|python-urllib", re.I)),
    ("curl/wget",     re.compile(r"^curl|^wget|libwww", re.I)),
]

# 疑似攻击路径关键词
ATTACK_PATHS = [
    (r"wp-admin|wp-login|wordpress",   "WordPress 后台探测"),
    (r"\.env|\.git/|\.svn/",           "敏感文件泄露探测"),
    (r"phpmyadmin|mysql|adminer",      "数据库管理入口探测"),
    (r"\.php$",                        "PHP 文件请求"),
    (r"union\s+select|select\s+from|or\s+1=1|information_schema", "SQL 注入探测"),
    (r"<script|alert\(|%3cscript|onerror=", "XSS 探测"),
    (r"\.\./|%2e%2e/|/etc/passwd",     "目录穿越"),
    (r"admin|login|admin_login",       "管理后台探测"),
    (r"\.zip|\.sql|\.bak|\.tar\.gz|\.conf", "备份/配置泄露探测"),
    (r"xmlrpc\.php",                   "xmlrpc 攻击"),
    (r"shell|cmd\.exe|wget\s+http",    "命令/后门探测"),
]

# 视为"非正常访问"的状态码
BAD_STATUS = {"403", "404", "405", "410", "499", "500", "502", "503", "504"}

# 异常探测 URI 过滤：静态资源扩展名（不含 .txt——robots.txt 是探测信号）
STATIC_EXTS = (
    ".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
    ".woff", ".woff2", ".webp", ".map", ".mp4", ".mp3", ".pdf", ".eot", ".ttf",
)

# 陌生 Host 判定：请求 Host 的注册域（剥离端口后取末两段）出现次数 < 该值视为陌生
# （第三方域名探测，如 vcards.top；本站多子域 blog./www. 会聚到同一注册域，不会被误判）
HOST_DOMAIN_KNOWN_MIN = 10

# 真实用户流量豁免：请求数 >= 该值 且 异常率 < 该比例的 UA 视为正常流量，取消特殊指纹标记
# （避免 Chrome/58 等老版本浏览器主力用户被"老版本伪装"误伤，连带污染风险 IP）
UA_NORMAL_MIN = 1000
UA_NORMAL_BAD_RATE = 0.05

# 输出规模
UA_TOP_N = 50
IP_TOP_N = 100
URI_TOP_N = 30

# 风险分权重
W_BAD = 1          # 异常状态码
W_ATTACK = 3       # 攻击路径探测
W_SPECIAL_UA = 2   # 特殊 UA
W_STRANGE_HOST = 2 # 陌生 Host 探测

# 特殊 UA 判定正则
MAIN_BROWSER_RE = re.compile(r"Mozilla|Chrome|Safari|Firefox|Edg|Opera|MSIE|Trident", re.I)
SUSPECT_WORD_RE = re.compile(
    r"bot|crawler|spider|curl|wget|python|urllib|httpclient|scrapy|go-http|"
    r"okhttp|java/|ruby|libwww|perl|nikto|sqlmap|masscan|zgrab|headless|"
    r"phantom|selenium|axios|node|fetch|httpx|postman|insomnia|apache-http",
    re.I,
)
OLD_BROWSER_RE = re.compile(r"(?:Firefox|Chrome|Safari)/(\d+)")


def reg_domain(host):
    """提取 Host 的注册域：剥离端口后取末两段（blog.catmes.com -> catmes.com；vcards.top -> vcards.top）。
    IPv6 方括号形式或 IP 直连原样返回。"""
    h = (host or "").strip()
    if not h:
        return ""
    if h.startswith("["):  # IPv6 [::1]:port
        h = h.strip("]").split("[")[-1].split(":")[0]
    elif ":" in h:       # 剥离端口
        h = h.rsplit(":", 1)[0]
    parts = h.split(".")
    if len(parts) >= 2:
        return ".".join(parts[-2:])
    return h


def classify_ua(ua):
    """返回 (known_name, special, reason)。

    known_name: 命中 KNOWN_UA_PATTERNS 的爬虫名，未命中为 None
    special:    是否疑似特殊指纹（需人工关注/候选入指纹库）
    reason:     special 判定理由（空串表示非特殊）
    """
    ua = (ua or "").strip()
    if not ua:
        return None, True, "空UA"
    for name, rx in KNOWN_UA_PATTERNS:
        if rx.search(ua):
            return name, False, ""
    if SUSPECT_WORD_RE.search(ua):
        return None, True, "疑似脚本/爬虫特征词"
    if not MAIN_BROWSER_RE.search(ua):
        return None, True, "非主流浏览器标识"
    m = OLD_BROWSER_RE.search(ua)
    if m and m.group(1).isdigit() and int(m.group(1)) < 60:
        return None, True, "老版本浏览器伪装"
    return None, False, ""


def iter_records(log_files):
    """流式读取多个日志文件，产出 (rec, line_no, file_name)。"""
    for path in log_files:
        if not os.path.exists(path):
            print(f"[警告] 日志文件不存在，跳过: {path}", file=sys.stderr)
            continue
        print(f"[读取] {path} ...", file=sys.stderr)
        with open(path, "r", encoding="utf-8", errors="replace") as fh:
            for i, line in enumerate(fh, 1):
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    continue
                yield rec


def main():
    base_dir = os.path.dirname(os.path.abspath(__file__))
    log_files = [f if os.path.isabs(f) else os.path.join(base_dir, f) for f in LOG_FILES]

    # ── 第一遍：Host 注册域分布（判定陌生 Host 用）──
    print("第一遍扫描：学习 Host 注册域分布 ...", file=sys.stderr)
    host_counter = Counter()
    domain_counter = Counter()
    total = 0
    for rec in iter_records(log_files):
        total += 1
        h = (rec.get("http_host") or "").strip()
        if h:
            host_counter[h] += 1
            domain_counter[reg_domain(h)] += 1
        if total % 200000 == 0:
            print(f"  已扫描 {total} 条 ...", file=sys.stderr)

    def is_strange_host(h):
        # 陌生 = 注册域低频（第三方域名探测；本站多子域聚合到同一注册域不会被误判）
        return bool(h) and domain_counter.get(reg_domain(h), 0) < HOST_DOMAIN_KNOWN_MIN

    # ── 第二遍：聚合全部维度 ──
    print("第二遍扫描：聚合明细 ...", file=sys.stderr)
    status_all = Counter()                     # 全部状态码分布
    known_ua_cat = Counter()                   # 已知爬虫类别聚合
    ua_stats = defaultdict(lambda: {"total": 0, "bad": 0})  # 完整 UA -> 统计
    ua_special = {}                            # 完整 UA -> (known, special, reason) 缓存判定
    ip_stats = defaultdict(lambda: {
        "total": 0, "bad": 0, "attack": 0,
        "special_ua": 0, "strange_host": 0,
    })                                         # IP -> 异常指标
    ip_special_ua = defaultdict(set)           # IP -> 使用的特殊 UA 集合（统计后豁免修正用）
    uri_attack = Counter()                     # 异常探测 URI
    attack_cat = Counter()                     # 攻击路径关键词命中

    for i, rec in enumerate(iter_records(log_files), 1):
        status = str(rec.get("status") or "?")
        uri = (rec.get("request_uri") or "").strip()
        ua = (rec.get("http_user_agent") or "").strip()
        ip = (rec.get("remote_addr") or "").strip()
        host = (rec.get("http_host") or "").strip()

        bad = status in BAD_STATUS
        status_all[status] += 1

        # UA 指纹
        if ua not in ua_special:
            ua_special[ua] = classify_ua(ua)
        known_name, special, reason = ua_special[ua]
        ua_stats[ua]["total"] += 1
        if bad:
            ua_stats[ua]["bad"] += 1
        if known_name:
            known_ua_cat[known_name] += 1

        # IP 聚合
        if ip:
            st = ip_stats[ip]
            st["total"] += 1
            if bad:
                st["bad"] += 1
            if special:
                st["special_ua"] += 1
                ip_special_ua[ip].add(ua)
            if is_strange_host(host):
                st["strange_host"] += 1

        # 攻击路径探测（命中即计，同时计入该 IP）
        hit = False
        for rx, desc in ATTACK_PATHS:
            if re.search(rx, uri, re.I):
                attack_cat[desc] += 1
                if ip:
                    ip_stats[ip]["attack"] += 1
                hit = True
                break

        # 异常探测 URI（过滤静态资源与 200 成功；命中攻击路径或异常状态都算探测）
        if uri:
            low = uri.lower()
            if not low.endswith(STATIC_EXTS) and (hit or status not in ("200", "301", "302")):
                uri_attack[uri] += 1

        if i % 200000 == 0:
            print(f"  已聚合 {i} 条 ...", file=sys.stderr)

    # ── 真实用户流量豁免修正 ──
    # 高请求数 + 低异常率的 UA（如 Chrome/58 老版本浏览器主力用户）视为正常流量，
    # 取消特殊指纹标记，避免误伤真实用户并连带污染风险 IP 统计。
    exempted = 0
    for ua, st in ua_stats.items():
        if ua_special[ua][1] and st["total"] >= UA_NORMAL_MIN \
                and st["bad"] / st["total"] < UA_NORMAL_BAD_RATE:
            ua_special[ua] = (None, False, "正常流量(高请求低异常,豁免)")
            exempted += 1
    if exempted:
        print(f"[修正] 豁免 {exempted} 种高请求低异常 UA 的特殊指纹标记（视为正常用户流量）", file=sys.stderr)
    # 重算每个 IP 的 special_ua（剔除已被豁免的 UA）
    for ip, ua_set in ip_special_ua.items():
        ip_stats[ip]["special_ua"] = sum(1 for u in ua_set if ua_special[u][1])

    # ── 输出报告 ──
    out = sys.stdout
    print(f"总请求数: {total}（合并 {len(log_files)} 个日志文件）")
    print("=" * 70)

    print("\n【1】状态码分布 Top 15:")
    for s, c in status_all.most_common(15):
        print(f"  {s:<6} {c}")

    print(f"\n【2】完整 UA 指纹 Top {UA_TOP_N}（原始 UA 分组；S=特殊指纹 Y/N，K=已知爬虫名/-）:")
    print(f"  {'请求数':>8} {'异常':>6} {'S':<3} {'K':<12} UA")
    for ua, st in sorted(ua_stats.items(), key=lambda kv: kv[1]["total"], reverse=True)[:UA_TOP_N]:
        kn, sp, rsn = ua_special[ua]
        s = "Y" if sp else "-"
        k = kn or "-"
        disp = ua[:110] + ("..." if len(ua) > 110 else "")
        print(f"  {st['total']:>8} {st['bad']:>6} {s:<3} {k:<12} {disp}")

    print(f"\n【2b】特殊 UA 指纹统计（未命中已知爬虫 + 异常特征，共 {sum(1 for v in ua_special.values() if v[1])} 种）:")
    special_list = sorted(
        ((ua, st, ua_special[ua][2]) for ua, st in ua_stats.items() if ua_special[ua][1]),
        key=lambda t: t[1]["total"], reverse=True,
    )
    for ua, st, rsn in special_list[:50]:
        print(f"  {st['total']:>6} [{rsn}] {ua[:100]}")

    print(f"\n【3】风险 IP 候选 Top {IP_TOP_N}（风险分=异常*{W_BAD}+攻击*{W_ATTACK}+特殊UA*{W_SPECIAL_UA}+陌生Host*{W_STRANGE_HOST}）:")
    print(f"  {'风险分':>6} {'总请求':>8} {'异常':>6} {'攻击':>6} {'特UA':>6} {'陌Host':>7} 失败率  IP")
    scored = []
    for ip, st in ip_stats.items():
        risk = (st["bad"] * W_BAD + st["attack"] * W_ATTACK
                + st["special_ua"] * W_SPECIAL_UA + st["strange_host"] * W_STRANGE_HOST)
        if risk <= 0:
            continue
        rate = f"{st['bad'] / st['total'] * 100:.1f}%" if st["total"] else "-"
        scored.append((risk, ip, st, rate))
    scored.sort(key=lambda t: t[0], reverse=True)
    for risk, ip, st, rate in scored[:IP_TOP_N]:
        print(f"  {risk:>6} {st['total']:>8} {st['bad']:>6} {st['attack']:>6} {st['special_ua']:>6} {st['strange_host']:>7} {rate:>6}  {ip}")

    print(f"\n【4】异常探测 URI Top {URI_TOP_N}（已过滤静态资源与 200/301/302）:")
    for u, c in uri_attack.most_common(URI_TOP_N):
        print(f"  {c:<8} {u[:110]}")

    print("\n【5】攻击路径关键词命中 Top 15:")
    for d, c in attack_cat.most_common(15):
        print(f"  {d:<22} {c}")

    print("\n【6】已知爬虫 UA 聚合 Top 20:")
    for name, c in known_ua_cat.most_common(20):
        print(f"  {name:<20} {c}")

    print("\n【7】陌生 Host 探测 Top 20（第三方域名/低频注册域，注册域出现次数 < "
          f"{HOST_DOMAIN_KNOWN_MIN} 视为陌生，已剥离端口按注册域判定）:")
    strange_hosts = sorted(((h, c) for h, c in host_counter.items()
                            if domain_counter.get(reg_domain(h), 0) < HOST_DOMAIN_KNOWN_MIN),
                           key=lambda t: t[1], reverse=True)
    for h, c in strange_hosts[:20]:
        print(f"  {c:<6} {h}")
    if not strange_hosts:
        print("  （无）")

    # ── 落盘产出文件 ──
    ua_file = os.path.join(base_dir, "report_ua_fingerprint_candidates.txt")
    with open(ua_file, "w", encoding="utf-8") as f:
        f.write("# 特殊 UA 指纹候选（人工审核后追加到 plugins/shield/rules/crawler_ua.txt）\n")
        f.write("# 格式：请求数[TAB]判定理由[TAB]UA 原文\n")
        for ua, st, rsn in special_list:
            f.write(f"{st['total']}\t{rsn}\t{ua}\n")
    print(f"\n[产出] 特殊 UA 指纹候选已写入: {ua_file}（{len(special_list)} 条）")

    ip_file = os.path.join(base_dir, "report_risk_ips.txt")
    with open(ip_file, "w", encoding="utf-8") as f:
        f.write("# 风险 IP 候选（人工确认后追加到 IP 黑名单；风险分公式见报告）\n")
        f.write("# 格式：风险分[TAB]IP[TAB]总请求[TAB]异常[TAB]攻击[TAB]特殊UA[TAB]陌生Host\n")
        for risk, ip, st, rate in scored:
            f.write(f"{risk}\t{ip}\t{st['total']}\t{st['bad']}\t{st['attack']}\t{st['special_ua']}\t{st['strange_host']}\n")
    print(f"[产出] 风险 IP 候选已写入: {ip_file}（{len(scored)} 条）")

    return 0


if __name__ == "__main__":
    sys.exit(main())
