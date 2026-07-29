// exporter.js —— 导出模块：TXT/CSV 下载（Blob）、剪贴板复制

function escapeCSV(value) {
    const str = String(value ?? '');
    if (str.includes(',') || str.includes('"') || str.includes('\n')) {
        return '"' + str.replace(/"/g, '""') + '"';
    }
    return str;
}

function triggerDownload(blob, filename) {
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
}

/** 下载纯文本（结果框内容） */
export function downloadAsText(content, filename = 'iptest-result.txt') {
    triggerDownload(new Blob([content], { type: 'text/plain;charset=utf-8' }), filename);
}

/**
 * 下载 CSV。
 * columns: [{key, label}]；带 BOM 头确保 Excel 打开中文不乱码。
 * 特殊取值：enableTLS 来自当前配置；downloadSpeedKBs 格式化为 "N kB/s"；tcpLatencyMs 为 "N ms"。
 */
export function downloadAsCSV(results, columns, { enableTLS = true, filename = 'iptest-result.csv' } = {}) {
    const BOM = '﻿';
    const header = columns.map(c => escapeCSV(c.label)).join(',');
    const rows = results.map(r => columns.map(c => {
        if (c.key === 'enableTLS') return String(enableTLS);
        if (c.key === 'downloadSpeedKBs') return r.downloadSpeedKBs ? `${r.downloadSpeedKBs.toFixed(0)} kB/s` : '';
        if (c.key === 'tcpLatencyMs') return r.tcpLatencyMs != null ? `${r.tcpLatencyMs} ms` : '';
        return escapeCSV(r[c.key]);
    }).join(','));
    const content = BOM + [header, ...rows].join('\r\n');
    triggerDownload(new Blob([content], { type: 'text/csv;charset=utf-8' }), filename);
}

export async function copyToClipboard(text) {
    await navigator.clipboard.writeText(text);
}
