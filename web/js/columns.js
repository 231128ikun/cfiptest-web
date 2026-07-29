// columns.js —— 列注册表：表格列与 CSV 列的唯一来源
//
// 改造前这里是两份互不相关的列表：table.js 的 COLUMNS(11 列) 与
// CSV_COLUMNS(27 列)，同一个字段的中文名在两处各写一遍，改一处忘另一处
// 就会不一致；index.html 里还硬编码了「共 27 列」这个字符串，加减列必然漂移。
//
// 现在合并为一份带 inTable / inCSV 标记的注册表，表格列与 CSV 列都从这里派生，
// 列数由 CSV_COLUMNS.length 算出而非手写。
//
// ALL_COLUMNS 的顺序 = CSV 导出顺序，与改造前的 27 列逐列对齐（导出文件的列
// 顺序是用户可见的，不能因为这次重构而变）。表格的列顺序与 CSV 不同，
// 单独由 TABLE_ORDER 指定。

/** 延迟徽章：按快/中/慢着色。 */
function latencyBadge(ms) {
    if (ms == null) return '';
    const cls = ms <= 150 ? 'fast' : ms <= 400 ? 'mid' : 'slow';
    return `<span class="badge ${cls}">${ms} ms</span>`;
}

/** 速度徽章：未测速时明确显示「未测」，避免与 0 kB/s 混淆。 */
function speedText(kbs) {
    if (!kbs) return '<span class="badge none">未测</span>';
    const cls = kbs >= 1000 ? 'fast' : kbs >= 100 ? 'mid' : 'slow';
    const text = kbs >= 1024 ? `${(kbs / 1024).toFixed(1)} MB/s` : `${kbs.toFixed(0)} kB/s`;
    return `<span class="badge ${cls}">${text}</span>`;
}

export function escapeHTML(s) {
    return String(s ?? '').replace(/[&<>"']/g, c =>
        ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

/**
 * 全部列定义，顺序即 CSV 导出顺序。
 *   key       结果对象上的字段名（'_sel' 是勾选框这一伪列）
 *   label     表格表头文字
 *   csvLabel  CSV 表头文字（与 label 不同时才写；历史上两者措辞有别）
 *   inTable   是否出现在结果表格
 *   inCSV     是否出现在 CSV 导出
 *   sortable  表格内是否可点击排序
 *   type      'number' 时按数值排序并右对齐
 *   render    表格单元格渲染函数（返回 HTML，需自行转义）
 *   csv       CSV 取值函数（缺省取 r[key]）
 */
export const ALL_COLUMNS = [
    // 勾选伪列：只在表格出现，不参与 CSV
    { key: '_sel', label: '', inTable: true, inCSV: false, sortable: false },

    { key: 'ip', label: 'IP 地址', csvLabel: 'IP地址', inTable: true, inCSV: true, sortable: true },
    { key: 'port', label: '端口', csvLabel: '端口号', inTable: true, inCSV: true, sortable: true, type: 'number' },
    // enableTLS 不在结果对象上，来自当次测试配置，由导出时通过 ctx 传入
    { key: 'enableTLS', label: 'TLS', inTable: false, inCSV: true, csv: (r, ctx) => String(ctx.enableTLS) },
    { key: 'dataCenter', label: '数据中心', inTable: true, inCSV: true, sortable: true },
    { key: 'locCode', label: 'IP位置', inTable: false, inCSV: true },
    { key: 'region', label: '地区', inTable: false, inCSV: true },
    { key: 'city', label: '城市', inTable: false, inCSV: true },
    { key: 'regionZh', label: '地区(中文)', inTable: false, inCSV: true },
    { key: 'country', label: '国家', csvLabel: '出站IP位置', inTable: true, inCSV: true, sortable: true },
    {
        key: 'cityZh', label: '城市', csvLabel: '城市(中文)', inTable: true, inCSV: true,
        sortable: true, render: r => escapeHTML(r.cityZh || r.city || ''),
    },
    { key: 'emoji', label: '国旗', inTable: true, inCSV: true, sortable: false },
    {
        key: 'tcpLatencyMs', label: '延迟', csvLabel: '网络延迟', inTable: true, inCSV: true,
        sortable: true, type: 'number', render: r => latencyBadge(r.tcpLatencyMs),
        csv: r => (r.tcpLatencyMs != null ? `${r.tcpLatencyMs} ms` : ''),
    },
    {
        key: 'downloadSpeedKBs', label: '速度', csvLabel: '下载速度', inTable: true, inCSV: true,
        sortable: true, type: 'number', render: r => speedText(r.downloadSpeedKBs),
        csv: r => (r.downloadSpeedKBs ? `${r.downloadSpeedKBs.toFixed(0)} kB/s` : ''),
    },
    { key: 'outboundIP', label: '出站IP', inTable: false, inCSV: true },
    { key: 'ipType', label: '出站', csvLabel: '出站IP类型', inTable: true, inCSV: true, sortable: true },
    { key: 'ipsType', label: 'IPS类型', inTable: false, inCSV: true },
    { key: 'asn', label: 'ASN号码', inTable: false, inCSV: true },
    { key: 'asnOrg', label: 'ASN 组织', csvLabel: 'ASN组织', inTable: true, inCSV: true, sortable: true },
    { key: 'visitScheme', label: '访问协议', inTable: false, inCSV: true },
    { key: 'tlsVersion', label: 'TLS版本', inTable: false, inCSV: true },
    { key: 'sni', label: 'SNI', inTable: false, inCSV: true },
    { key: 'httpVersion', label: 'HTTP版本', inTable: false, inCSV: true },
    { key: 'warp', label: 'WARP', inTable: false, inCSV: true },
    { key: 'gateway', label: 'Gateway', inTable: false, inCSV: true },
    { key: 'rbi', label: 'RBI', inTable: false, inCSV: true },
    { key: 'kex', label: '密钥交换', inTable: false, inCSV: true },
    { key: 'timestamp', label: '时间戳', inTable: false, inCSV: true },
];

/**
 * 表格列的显示顺序（与 CSV 顺序不同：表格把延迟/速度前置，便于比较）。
 * 与改造前 table.js 的 COLUMNS 顺序一致。
 */
const TABLE_ORDER = [
    '_sel', 'ip', 'port', 'tcpLatencyMs', 'downloadSpeedKBs',
    'dataCenter', 'emoji', 'country', 'cityZh', 'ipType', 'asnOrg',
];

const byKey = new Map(ALL_COLUMNS.map(c => [c.key, c]));

/** 结果表格使用的列（含勾选伪列），按 TABLE_ORDER 排列。 */
export const TABLE_COLUMNS = TABLE_ORDER.map(key => {
    const col = byKey.get(key);
    if (!col) throw new Error(`TABLE_ORDER 含未定义的列: ${key}`);
    if (!col.inTable) throw new Error(`列 ${key} 未标记 inTable，却出现在 TABLE_ORDER`);
    return col;
});

/** CSV 导出可选的列，label 统一为 csvLabel ?? label。 */
export const CSV_COLUMNS = ALL_COLUMNS
    .filter(c => c.inCSV)
    .map(c => ({ key: c.key, label: c.csvLabel ?? c.label }));

/** 取某列的 CSV 值；ctx 提供结果对象之外的上下文（如 enableTLS）。 */
export function csvValue(col, result, ctx = {}) {
    const def = byKey.get(col.key);
    if (def?.csv) return def.csv(result, ctx);
    return result[col.key];
}

/** 按 key 查列定义（表格排序需要读 type / sortable）。 */
export function columnByKey(key) {
    return byKey.get(key);
}
