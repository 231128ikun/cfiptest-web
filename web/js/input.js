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
 * 规范化单行输入为 "IP:PORT"（IPv6 为 "[IP]:PORT"）；无法识别返回 null。
 * 支持：1.2.3.4:443 / 1.2.3.4 / 1.2.3.4 443 / 中文冒号 / [v6]:443 / 纯v6 /
 *       行内 # 注释（丢弃）/ CSV 元数据行（取首列）
 */
export function normalizeIPFormat(input) {
    if (!input) return null;
    input = input.trim();
    if (!input || input.startsWith('#')) return null;

    // 去掉行内注释
    let mainPart = input;
    const commentIndex = input.indexOf('#');
    if (commentIndex > 0) mainPart = input.substring(0, commentIndex).trim();

    // CSV 元数据行：取首列递归
    const fields = mainPart.split(',').map(s => s.trim());
    if (fields.length > 1) return normalizeIPFormat(fields[0]);

    // [IPv6]:port
    let match = mainPart.match(/^\[([0-9a-fA-F:]+)\]:(\d+)$/);
    if (match && isValidPort(match[2])) return `[${match[1]}]:${match[2]}`;

    // 纯 IPv6 → 默认 443
    if (/^[0-9a-fA-F:]+$/.test(mainPart) && mainPart.includes(':') && !mainPart.includes('.')) {
        return `[${mainPart.replace(/^\[/, '').replace(/\]$/, '')}]:443`;
    }

    // IPv4:port
    match = mainPart.match(/^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}):(\d+)$/);
    if (match && isValidIPv4(match[1]) && isValidPort(match[2])) return `${match[1]}:${match[2]}`;

    // 中文冒号
    match = mainPart.match(/^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})：(\d+)$/);
    if (match && isValidIPv4(match[1]) && isValidPort(match[2])) return `${match[1]}:${match[2]}`;

    // 空格分隔
    const parts = mainPart.split(/\s+/);
    if (parts.length === 2 && /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(parts[0])
        && /^\d+$/.test(parts[1]) && isValidIPv4(parts[0]) && isValidPort(parts[1])) {
        return `${parts[0]}:${parts[1]}`;
    }

    // 纯 IPv4 → 默认 443
    if (/^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(mainPart) && isValidIPv4(mainPart)) {
        return `${mainPart}:443`;
    }

    // 兜底：IPv4 + 非数字分隔 + 数字
    match = mainPart.match(/(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\D+(\d+)/);
    if (match && isValidIPv4(match[1]) && isValidPort(match[2])) return `${match[1]}:${match[2]}`;

    return null;
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
 * 语法：空格=且，逗号=或，| = 分组或
 * 例："port:443,8443 country:JP,US 东京" / "port:443 | port:2053"
 * 支持键：port（支持 800-900 范围）、country、asn/as、关键词（任意文本）
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
    const tokens = String(query || '').split(/\s+/).map(v => v.trim()).filter(Boolean);
    if (!tokens.length) return null;
    const criteria = { ports: [], countries: [], asns: [], text: [] };
    for (const token of tokens) {
        const match = token.match(/^([a-zA-Z]+):(.*)$/);
        if (!match) { criteria.text.push(token.toLowerCase()); continue; }
        const key = match[1].toLowerCase();
        const values = match[2].split(',').map(v => v.trim()).filter(Boolean);
        if (!values.length) continue;
        if (key === 'port') {
            const parsed = parsePortFilter(values.join(','));
            if (!parsed) return null;
            criteria.ports.push(...parsed);
        } else if (key === 'country') {
            criteria.countries.push(...values.map(v => v.toUpperCase()));
        } else if (key === 'asn' || key === 'as') {
            criteria.asns.push(...values.map(v => v.replace(/^AS/i, '').toUpperCase()));
        } else {
            criteria.text.push(token.toLowerCase());
        }
    }
    return criteria;
}

/** 解析完整筛选表达式（| 分组或） */
export function parseFilterExpression(query) {
    const groups = String(query || '').split('|').map(parseUniversalFilter).filter(Boolean);
    return groups.length ? groups : null;
}

/** 判断一行（已规范化的 IP:PORT 文本）是否匹配条件组 */
export function lineMatchesFilter(line, criteria) {
    if (Array.isArray(criteria)) return criteria.some(g => lineMatchesFilter(line, g));

    const portMatch = line.match(/:(\d+)$/);
    const portNum = portMatch ? parseInt(portMatch[1], 10) : NaN;
    if (criteria.ports.length && !criteria.ports.some(p =>
        typeof p === 'number' ? portNum === p : portNum >= p.start && portNum <= p.end)) {
        return false;
    }
    if (criteria.countries.length || criteria.asns.length) {
        // 输入阶段的行通常只有 IP:PORT，没有国家/ASN 元数据
        const upper = line.toUpperCase();
        if (criteria.countries.length && !criteria.countries.some(c => upper.includes(c))) return false;
        if (criteria.asns.length && !criteria.asns.some(a => upper.includes(a))) return false;
    }
    if (criteria.text.length) {
        const lower = line.toLowerCase();
        if (!criteria.text.some(t => lower.includes(t))) return false;
    }
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
