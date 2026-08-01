// exporter.js —— 导出模块：TXT/CSV 下载（Blob）、剪贴板复制

import { csvValue } from './columns.js';
import { formatResults } from './composer.js';

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

/** 投递任意文本为文件；TXT/CSV 都从这一条路走，未来上传云也复用同一份 content。 */
export function download(content, filename, type = 'text/plain;charset=utf-8') {
    triggerDownload(new Blob([content], { type }), filename);
}

/** 下载纯文本（结果框内容） */
export function downloadAsText(content, filename = 'iptest-result.txt') {
    return download(content, filename, 'text/plain;charset=utf-8');
}

/**
 * 把结果序列化为指定格式的字符串（纯函数，不碰 DOM）。
 *
 * 与「如何投递」（浏览器下载 / 将来上传云端）解耦：下载路径只负责把
 * 这里的返回值包进 Blob，未来要上传就直接复用同一个字符串。
 *
 * format='txt' 时用 ctx.template 逐行替换；format='csv' 时 columns 为 [{key, label}]，
 * ctx 提供结果对象之外的上下文（如 enableTLS）。
 */
export function serialize(results, format, { columns = [], ...ctx } = {}) {
    if (format === 'txt') return formatResults(ctx.template || '', results);
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
    const BOM = '\uFEFF';
    const content = BOM + serialize(results, 'csv', { columns, enableTLS });
    return download(content, filename, 'text/csv;charset=utf-8');
}

export async function copyToClipboard(text) {
    if (!navigator.clipboard?.writeText) {
        throw new Error('当前浏览器不支持剪贴板写入，请手动复制预览内容');
    }
    try {
        await navigator.clipboard.writeText(text);
    } catch {
        throw new Error('无法写入剪贴板，请检查浏览器权限或手动复制预览内容');
    }
}
