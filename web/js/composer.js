// composer.js —— 结果框模板引擎：占位符替换 + 预设格式

const PLACEHOLDERS = {
    '{ip}': r => r.ip ?? '',
    '{port}': r => r.port ?? '',
    '{country}': r => r.country ?? '',
    '{emoji}': r => r.emoji ?? '',
    '{city}': r => r.cityZh || r.city || '',
    '{latency}': r => r.tcpLatencyMs ?? '',
    '{speed}': r => r.downloadSpeedKBs ? r.downloadSpeedKBs.toFixed(0) : '',
    '{dc}': r => r.dataCenter ?? '',
    '{asn}': r => r.asn || '',
    '{asnOrg}': r => r.asnOrg ?? '',
    '{ipType}': r => r.ipType ?? '',
    '{outboundIP}': r => r.outboundIP ?? '',
    '{tls}': r => r.tlsVersion ?? '',
    '{http}': r => r.httpVersion ?? '',
};

export const PRESETS = [
    { name: 'IP:Port#国家', template: '{ip}:{port}#{emoji}{country}' },
    { name: 'IP:Port', template: '{ip}:{port}' },
    { name: 'IP Port（空格）', template: '{ip} {port}' },
    { name: 'IP:Port#国家-延迟', template: '{ip}:{port}#{emoji}{country} {latency}ms' },
    { name: '含速度', template: '{ip}:{port}#{emoji}{country} {latency}ms {speed}kB/s' },
];

/** 对单条结果应用模板 */
export function formatResult(template, result) {
    let out = template;
    for (const [ph, getter] of Object.entries(PLACEHOLDERS)) {
        out = out.split(ph).join(String(getter(result)));
    }
    return out;
}

/** 批量应用模板，一行一条 */
export function formatResults(template, results) {
    return results.map(r => formatResult(template, r)).join('\n');
}

/** 模板中可用的占位符列表（供帮助提示渲染） */
export function placeholderNames() {
    return Object.keys(PLACEHOLDERS);
}
