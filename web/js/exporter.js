// exporter.js —— 导出模块：TXT/CSV 下载（Blob）、剪贴板复制

import { csvValue } from './columns.js';

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
 * 把结果序列化为指定格式的字符串（纯函数，不碰 DOM）。
 *
 * 与「如何投递」（浏览器下载 / 将来上传云端）解耦：下载路径只负责把
 * 这里的返回值包进 Blob，未来要上传就直接复用同一个字符串。
 *
 * columns: [{key, label}]；ctx 提供结果对象之外的上下文（如 enableTLS）。
 */
export function serialize(results, format, { columns = [], ...ctx } = {}) {
    if (format !== 'csv') throw new Error(`不支持的导出格式: ${format}`);
    const header = columns.map(c => escapeCSV(c.label)).join(',');
    const rows = results.map(r =>
        columns.map(c => escapeCSV(csvValue(c, r, ctx))).join(','));
    return [header, ...rows].join('\r\n');
}

/**
 * 下载 CSV。
 * columns: [{key, label}]；带 BOM 头确保 Excel 打开中文不乱码。
 * 各列的取值规则（TLS 来自配置、速度/延迟带单位）由 columns.js 的注册表定义。
 */
export function downloadAsCSV(results, columns, { enableTLS = true, filename = 'iptest-result.csv' } = {}) {
    const BOM = '﻿';
    const content = BOM + serialize(results, 'csv', { columns, enableTLS });
    triggerDownload(new Blob([content], { type: 'text/csv;charset=utf-8' }), filename);
}

export async function copyToClipboard(text) {
    await navigator.clipboard.writeText(text);
}
