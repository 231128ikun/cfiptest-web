// input.js —— 输入整理模块：IP 文本规范化、去重、筛选 DSL
// 移植并扩展自 DDNS-cf-proxyip 前端的 normalizeIPFormat / smartFilter 逻辑。

/** 校验 IPv4 每段 0-255 */
function isValidIPv4(ip) {
    return ip.split('.').every(o => { const n = Number(o); return o !== '' && n >= 0 && n <= 255; });
}

/** 校验端口 1-65535 */
function isValidPort(port) {
    const n = Number(port);
    return Number.isInteger(n) && n >= 1 && n <= 65535;
}

/**
 * 规范化单行输入。显式端口保留为 "IP:PORT"（IPv6 为 "[IP]:PORT"）；
 * 没写端口时只返回 IP，实际端口在执行阶段根据 TLS 规则补 443/80。
 * 支持：1.2.3.4:443 / 1.2.3.4 / 1.2.3.4 443 / 中文冒号 / [v6]:443 / 纯v6 /
 *       行内 # 注释（丢弃）/ CSV 导出行（识别 IP、端口、国家列）
 */
export function normalizeIPFormat(input) {
    if (!input) return null;
    input = input.trim();
    if (!input || input.startsWith('#')) return null;

	// CSV 优先于 # 注释处理，避免国家/备注列影响地址识别。
	if (input.includes(',')) {
		const imported = csvRowToInputLine(input);
		if (imported) input = imported;
	}

	// 去掉行内注释
    let mainPart = input;
    const commentIndex = input.indexOf('#');
    if (commentIndex > 0) mainPart = input.substring(0, commentIndex).trim();

    // [IPv6]:port
    let match = mainPart.match(/^\[([0-9a-fA-F:]+)\]:(\d+)$/);
    if (match && isValidPort(match[2])) return `[${match[1]}]:${match[2]}`;

    // 纯 IPv6 → 保持未指定端口
    if (/^[0-9a-fA-F:]+$/.test(mainPart) && mainPart.includes(':') && !mainPart.includes('.')) {
        return mainPart.replace(/^\[/, '').replace(/\]$/, '');
    }

    // IPv4:port
    match = mainPart.match(/^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d+)$/);
    if (match && isValidIPv4(match[1]) && isValidPort(match[2])) return `${match[1]}:${match[2]}`;

    // 中文冒号
    match = mainPart.match(/^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})：(\d+)$/);
    if (match && isValidIPv4(match[1]) && isValidPort(match[2])) return `${match[1]}:${match[2]}`;

    // 空格分隔
    const parts = mainPart.split(/\s+/);
	if (parts.length >= 2 && /^\d{1,3}(?:\.\d{1,3}){3}$/.test(parts[0])
		&& /^\d+$/.test(parts[1]) && isValidIPv4(parts[0]) && isValidPort(parts[1])) {
		return `${parts[0]}:${parts[1]}`;
	}
    if (parts.length === 2 && /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(parts[0])
        && /^\d+$/.test(parts[1]) && isValidIPv4(parts[0]) && isValidPort(parts[1])) {
        return `${parts[0]}:${parts[1]}`;
    }

    // 纯 IPv4 → 保持未指定端口
    if (/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(mainPart) && isValidIPv4(mainPart)) {
        return mainPart;
    }

    // 兜底：IPv4 + 非数字分隔 + 数字
    match = mainPart.match(/(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\D+(\d+)/);
    if (match && isValidIPv4(match[1]) && isValidPort(match[2])) return `${match[1]}:${match[2]}`;

    return null;
}

function parseCSVRow(line) {
	const cells = [];
	let cell = '', quoted = false;
	for (let i = 0; i < line.length; i++) {
		const ch = line[i];
		if (ch === '"') {
			if (quoted && line[i + 1] === '"') { cell += '"'; i++; }
			else quoted = !quoted;
		} else if (ch === ',' && !quoted) {
			cells.push(cell.trim()); cell = '';
		} else cell += ch;
	}
	cells.push(cell.trim());
	return cells;
}

const normHeader = value => String(value || '').replace(/^\uFEFF/, '').trim().toLowerCase().replace(/[\s_()-]/g, '');
const IP_HEADERS = new Set(['ip', 'ip地址', 'ipaddress']);
const PORT_HEADERS = new Set(['port', '端口', '端口号']);
const COUNTRY_HEADERS = ['country', '国家', '出站ip位置'];
const CITY_HEADERS = ['cityzh', '城市中文', 'city', '城市'];

function findHeader(headers, aliases) {
	for (const alias of aliases) {
		const index = headers.indexOf(alias);
		if (index >= 0) return index;
	}
	return -1;
}

function formatImportedAddress(ip, port) {
	return ip.includes(':') ? `[${ip}]:${port}` : `${ip}:${port}`;
}

/** 将本程序导出的 CSV 转成输入框的一行：IP PORT COUNTRY。 */
export function csvRowToInputLine(line, headerMap = null) {
	const cells = parseCSVRow(String(line || ''));
	if (cells.length < 2) return null;
	let ipIndex = 0, portIndex = 1, countryIndex = 2, cityIndex = 3;
	if (headerMap) ({ ip: ipIndex, port: portIndex, country: countryIndex, city: cityIndex } = headerMap);
	const ip = cells[ipIndex]?.trim();
	const port = cells[portIndex]?.trim();
	if (!ip || !isValidPort(port)) return null;
	const country = countryIndex >= 0 ? cells[countryIndex]?.trim() : '';
	const city = cityIndex >= 0 ? cells[cityIndex]?.trim() : '';
	const location = [country, city].filter(Boolean).join('-').replaceAll('#', '');
	return `${formatImportedAddress(ip, port)}${location ? `#${location}` : ''}`;
}

/** 识别 CSV 表头并输出适合输入框筛选/查看的文本。 */
export function importCSVText(rawText) {
	const lines = String(rawText || '').split(/\r?\n/).filter(line => line.trim());
	if (!lines.length) return '';
	const headers = parseCSVRow(lines[0]).map(normHeader);
	const map = {
		ip: headers.findIndex(value => IP_HEADERS.has(value)),
		port: headers.findIndex(value => PORT_HEADERS.has(value)),
		country: findHeader(headers, COUNTRY_HEADERS),
		city: findHeader(headers, CITY_HEADERS),
	};
	const hasHeader = map.ip >= 0 && map.port >= 0;
	const rows = hasHeader ? lines.slice(1) : lines;
	const fallback = hasHeader ? map : { ip: 0, port: 1, country: 2, city: 3 };
	return rows.map(line => csvRowToInputLine(line, fallback)).filter(Boolean).join('\n');
}

/** 批量规范化：返回 { valid: 规范化后的唯一列表, invalidCount, dupCount } */
export function processInput(rawText) {
    const lines = rawText.split('\n');
    const seen = new Set();
    const valid = [];
    let invalidCount = 0;
    let dupCount = 0;

    for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed || trimmed.startsWith('#')) continue;
        const normalized = normalizeIPFormat(trimmed);
        if (!normalized) { invalidCount++; continue; }
        if (seen.has(normalized)) { dupCount++; continue; }
        seen.add(normalized);
        valid.push(normalized);
    }
    return { valid, invalidCount, dupCount, totalLines: lines.filter(l => l.trim()).length };
}

/** 简单去重（保持行原样，不做规范化） */
export function quickDeduplicate(lines) {
    return [...new Set(lines.map(l => l.trim()).filter(Boolean))];
}

/* ---------------- 筛选 DSL ----------------
 * 语法：空格或 && = 且，| 或 || = 条件组之间的或，逗号 = 同字段多个值。
 * 排除：条件前加 -/!，或使用 !=。例如 -port:9443、country!=美国、-测试。
 * 例："port:443,8443 country:日本 -测试" / "port:8000-9000 | port:2053"
 * 支持键：port（精确值或范围）、country、asn/as、关键词（任意文本）。
 */

function parsePortFilter(portStr) {
    const parts = portStr.split(',').map(p => p.trim()).filter(Boolean);
    const result = [];
    for (const part of parts) {
        if (part.includes('-')) {
            const [start, end] = part.split('-').map(p => parseInt(p.trim(), 10));
            if (!start || !end || start < 1 || end > 65535 || start > end) return null;
            result.push({ start, end });
        } else if (/^\d+$/.test(part)) {
            const n = parseInt(part, 10);
            if (n < 1 || n > 65535) return null;
            result.push(n);
        } else {
            return null;
        }
    }
    return result.length ? result : null;
}

function parseUniversalFilter(query) {
    const tokens = String(query || '').replace(/&&/g, ' ').split(/\s+/).map(v => v.trim()).filter(Boolean);
    if (!tokens.length) return null;
    const criteria = {
        ports: [], countries: [], asns: [], text: [],
        excludePorts: [], excludeCountries: [], excludeASNs: [], excludeText: [],
    };
    for (let token of tokens) {
        let excluded = token.startsWith('-') || token.startsWith('!');
        if (excluded) token = token.slice(1);
        const match = token.match(/^([a-zA-Z]+)(!=|:|=)(.*)$/);
        if (!match) {
            (excluded ? criteria.excludeText : criteria.text).push(token.toLowerCase());
            continue;
        }
        const key = match[1].toLowerCase();
        excluded ||= match[2] === '!=';
        const values = match[3].split(',').map(v => v.trim().replace(/^['"]|['"]$/g, '')).filter(Boolean);
        if (!values.length) continue;
        if (key === 'port') {
            const parsed = parsePortFilter(values.join(','));
            if (!parsed) return null;
            (excluded ? criteria.excludePorts : criteria.ports).push(...parsed);
        } else if (key === 'country') {
            (excluded ? criteria.excludeCountries : criteria.countries).push(...values.map(v => v.toUpperCase()));
        } else if (key === 'asn' || key === 'as') {
            (excluded ? criteria.excludeASNs : criteria.asns).push(...values.map(v => v.replace(/^AS/i, '').toUpperCase()));
        } else {
            (excluded ? criteria.excludeText : criteria.text).push(token.toLowerCase());
        }
    }
    return criteria;
}

/** 解析完整筛选表达式（| 分组或） */
export function parseFilterExpression(query) {
    const normalized = String(query || '').replace(/\|\|/g, '|');
    const groups = normalized.split('|').map(parseUniversalFilter).filter(Boolean);
    return groups.length ? groups : null;
}

function linePort(line) {
    const text = String(line || '').trim();
    const patterns = [
        /^\[[^\]]+\]:(\d+)(?:\s|#|$)/,
        /^\d{1,3}(?:\.\d{1,3}){3}[:：](\d+)(?:\s|#|$)/,
        /^(?:\[[^\]]+\]|\S+)\s+(\d+)(?:\s|#|$)/,
    ];
    for (const pattern of patterns) {
        const match = text.match(pattern);
        if (match) return Number(match[1]);
    }
    return NaN;
}

function portMatches(port, rules) {
    return rules.some(rule => typeof rule === 'number'
        ? port === rule
        : port >= rule.start && port <= rule.end);
}

/** 判断一行（已规范化的 IP:PORT 文本）是否匹配条件组 */
export function lineMatchesFilter(line, criteria) {
    if (Array.isArray(criteria)) return criteria.some(g => lineMatchesFilter(line, g));

    const portNum = linePort(line);
    if (criteria.ports.length && !portMatches(portNum, criteria.ports)) {
        return false;
    }
    if (criteria.excludePorts.length && portMatches(portNum, criteria.excludePorts)) return false;
    const upper = line.toUpperCase();
    if (criteria.countries.length || criteria.asns.length) {
        // 输入阶段的行通常只有 IP:PORT，没有国家/ASN 元数据
        if (criteria.countries.length && !criteria.countries.some(c => upper.includes(c))) return false;
        if (criteria.asns.length && !criteria.asns.some(a => upper.includes(a))) return false;
    }
    if (criteria.excludeCountries.some(c => upper.includes(c))) return false;
    if (criteria.excludeASNs.some(a => upper.includes(a))) return false;
    if (criteria.text.length) {
        // 裸关键词之间是「且」：占位符与文档都写明「空格=且」，
        // 改造前这里用 .some() 实际是「或」，"东京 443" 会匹配只含 443 的行。
        const lower = line.toLowerCase();
        if (!criteria.text.every(t => lower.includes(t))) return false;
    }
    const lower = line.toLowerCase();
    if (criteria.excludeText.some(t => lower.includes(t))) return false;
    return true;
}

/** 对行列表执行筛选；mode='keep' 保留匹配，'remove' 剔除匹配 */
export function smartFilter(lines, expression, mode = 'keep') {
    const criteria = parseFilterExpression(expression);
    if (!criteria) return null;
    return lines.filter(line => {
        const matched = lineMatchesFilter(line, criteria);
        return mode === 'keep' ? matched : !matched;
    });
}

/** 统计行列表的端口分布与 IP 类型分布 */
export function getInputStats(lines) {
    const ports = {};
    let v4 = 0, v6 = 0;
    for (const line of lines) {
        const m = line.match(/:(\d+)$/);
        if (m) ports[m[1]] = (ports[m[1]] || 0) + 1;
        if (line.startsWith('[')) v6++; else v4++;
    }
    return { total: lines.length, ports, v4, v6 };
}
/** CSV 表头别名 → 结果字段 key（兼容本程序导出的 CSV 与常见英文表头） */
const CSV_FIELD_ALIASES = new Map(Object.entries({
    ip: 'ip', ip地址: 'ip', ipaddress: 'ip',
    port: 'port', 端口: 'port', 端口号: 'port',
    datacenter: 'dataCenter', 数据中心: 'dataCenter', colo: 'dataCenter', dc: 'dataCenter',
    loccode: 'locCode', ip位置: 'locCode', loc: 'locCode',
    region: 'region', 地区: 'region',
    city: 'city', 城市: 'city',
    regionzh: 'regionZh', 地区中文: 'regionZh',
    country: 'country', 国家: 'country', 出站ip位置: 'country',
    cityzh: 'cityZh', 城市中文: 'cityZh',
    emoji: 'emoji', 国旗: 'emoji',
    tcplatencyms: 'tcpLatencyMs', 网络延迟: 'tcpLatencyMs', 延迟: 'tcpLatencyMs', latency: 'tcpLatencyMs',
    downloadspeedkbs: 'downloadSpeedKBs', 下载速度: 'downloadSpeedKBs', 速度: 'downloadSpeedKBs', speed: 'downloadSpeedKBs',
    outboundip: 'outboundIP', 出站ip: 'outboundIP',
    iptype: 'ipType', 出站ip类型: 'ipType', 出站类型: 'ipType',
    ipstype: 'ipsType', ips类型: 'ipsType',
    asn: 'asn', asn号码: 'asn',
    asnorg: 'asnOrg', asn组织: 'asnOrg',
    visitscheme: 'visitScheme', 访问协议: 'visitScheme',
    tlsversion: 'tlsVersion', tls版本: 'tlsVersion',
    sni: 'sni',
    httpversion: 'httpVersion', http版本: 'httpVersion',
    warp: 'warp',
    gateway: 'gateway',
    rbi: 'rbi',
    kex: 'kex', 密钥交换: 'kex',
    timestamp: 'timestamp', 时间戳: 'timestamp',
    countrycode: 'countryCode', 国家代码: 'countryCode',
}));

/** 解析数值单元格："123 ms"、"1234 kB/s"、"1.2 MB/s"、"123"；无效返回 NaN */
function parseNumericCell(value) {
    const raw = String(value ?? '').trim();
    if (!raw || /^[—\-]$/.test(raw) || /^(n\/?a|未知|未测)$/i.test(raw)) return NaN;
    const num = parseFloat(raw.replace(/,/g, ''));
    if (!Number.isFinite(num)) return NaN;
    return /mb/i.test(raw) ? num * 1024 : num;
}

/** 解析 IP 单元格："1.2.3.4" / "[2606:4700::1]" / "1.2.3.4:443" → { ip, port } */
function splitIPCell(value) {
    const cell = String(value ?? '').trim().replace(/^\uFEFF/, '');
    if (!cell) return null;
    if (cell.includes(':')) {
        const m = cell.match(/^\[?([0-9a-fA-F:.]+)\]?:(\d{1,5})$/);
        if (m && isValidPort(m[2])) return { ip: m[1], port: Number(m[2]) };
        const m2 = cell.match(/^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d{1,5})$/);
        if (m2 && isValidIPv4(m2[1]) && isValidPort(m2[2])) return { ip: m2[1], port: Number(m2[2]) };
        return { ip: cell.replace(/^\[|\]$/g, ''), port: NaN };
    }
    return { ip: cell, port: NaN };
}

/** 解析 CSV 全字段：返回 { entries, invalid, total }；entries 为带 ip/port 及可选字段的对象数组 */
export function parseCSVEntries(rawText) {
    const lines = String(rawText || '').split(/\r?\n/).map(line => line.trim()).filter(Boolean);
    const out = { entries: [], invalid: 0, total: 0 };
    if (!lines.length) return out;
    const first = parseCSVRow(lines[0]);
    const headers = first.map(normHeader);
    const ipIdx = headers.findIndex(h => h === 'ip' || h === 'ip地址' || h === 'ipaddress');
    const portIdx = headers.findIndex(h => h === 'port' || h === '端口' || h === '端口号');
    let colMap = null;
    let start = 0;
    if (ipIdx >= 0 && portIdx >= 0) {
        colMap = {};
        headers.forEach((h, i) => { const key = CSV_FIELD_ALIASES.get(h); if (key) colMap[i] = key; });
        start = 1;
    }
    out.total = lines.length - start;
    const cell = (row, idx) => String(row[idx] ?? '').trim().replace(/^\uFEFF/, '');
    for (let i = start; i < lines.length; i++) {
        const row = parseCSVRow(lines[i]);
        const rawIP = colMap ? cell(row, ipIdx) : cell(row, 0);
        const rawPort = colMap ? cell(row, portIdx) : cell(row, 1);
        const parsed = splitIPCell(rawIP);
        if (!parsed || !parsed.ip) { out.invalid++; continue; }
        let port = Number.isInteger(Number(rawPort)) && Number(rawPort) > 0 ? Number(rawPort) : parsed.port;
        if (!Number.isInteger(port) || port <= 0 || port > 65535) { out.invalid++; continue; }
        const entry = { ip: parsed.ip, port };
        if (colMap) {
            for (const [idx, key] of Object.entries(colMap)) {
                if (key === 'ip' || key === 'port') continue;
                const value = cell(row, Number(idx));
                if (!value) continue;
                if (key === 'tcpLatencyMs' || key === 'downloadSpeedKBs') {
                    const num = parseNumericCell(value);
                    if (Number.isFinite(num) && num > 0) entry[key] = Math.round(num);
                } else if (key === 'asn') {
                    const num = parseInt(String(value).replace(/[^\d]/g, ''), 10);
                    if (Number.isFinite(num) && num > 0) entry[key] = num;
                } else {
                    entry[key] = value;
                }
            }
        }
        out.entries.push(entry);
    }
    return out;
}
