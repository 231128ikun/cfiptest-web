// templates.js —— 输出模板（内置 PRESETS + 「我的模板」）的加载、保存与选项渲染。
// 工作台导出面板与任务编辑器共用同一份模板状态，避免两处各自维护。

import { PRESETS } from './composer.js';
import { escapeHTML } from './columns.js';
import * as api from './api.js';

export const SAVED_TEMPLATE_KEY = 'iptest.savedTemplates.v1';

const isTemplate = item => item && typeof item.name === 'string' && typeof item.template === 'string';

/** 从浏览器缓存读取「我的模板」（兼容旧版；主存储已迁移到 settings.json）。 */
export function loadSavedTemplates() {
    try {
        const parsed = JSON.parse(localStorage.getItem(SAVED_TEMPLATE_KEY) || '[]');
        return Array.isArray(parsed) ? parsed.filter(isTemplate) : [];
    } catch {
        return [];
    }
}

/** 从 settings.json 读取「我的模板」；读取失败返回 null（不覆盖本地数据）。 */
export async function fetchSettingsTemplates() {
    try {
        const config = await api.fetchConfig();
        const list = config?.settings?.savedTemplates;
        return Array.isArray(list) ? list.filter(isTemplate) : [];
    } catch {
        return null;
    }
}

/** 保存「我的模板」：先写浏览器缓存（兼容旧版回退），再写入 settings.json。 */
export async function persistTemplates(saved) {
    localStorage.setItem(SAVED_TEMPLATE_KEY, JSON.stringify(saved));
    await api.saveSettingsPatch({ savedTemplates: saved });
    return saved;
}

/** 模板内容 → 下拉选项值（preset:N / saved:N / custom）。 */
export function templateOptionFor(template, saved) {
    const p = PRESETS.findIndex(x => x.template === template);
    if (p >= 0) return `preset:${p}`;
    const s = saved.findIndex(x => x.template === template);
    if (s >= 0) return `saved:${s}`;
    return 'custom';
}

/** 下拉选项值 → 模板内容。 */
export function templateContentFor(optionValue, saved) {
    if (typeof optionValue === 'string' && optionValue.startsWith('preset:')) return PRESETS[Number(optionValue.slice(7))]?.template ?? '';
    if (typeof optionValue === 'string' && optionValue.startsWith('saved:')) return saved[Number(optionValue.slice(6))]?.template ?? '';
    return '';
}

/** 生成「内置模板 + 我的模板」的 <optgroup> 选项 HTML。 */
export function templateOptionsHTML(saved, { includeCustom = false } = {}) {
    const presetOpts = PRESETS.map((p, i) => `<option value="preset:${i}">${escapeHTML(p.name)}</option>`).join('');
    const savedOpts = saved.map((p, i) => `<option value="saved:${i}">${escapeHTML(p.name)}</option>`).join('');
    return `<optgroup label="内置模板">${presetOpts}</optgroup>`
        + (savedOpts ? `<optgroup label="我的模板">${savedOpts}</optgroup>` : '')
        + (includeCustom ? '<optgroup label="自定义"><option value="custom">自定义…</option></optgroup>' : '');
}

/** 把选项 HTML 填进 select 并回填当前值。 */
export function renderTemplateSelect(select, saved, selected = '', { includeCustom = false } = {}) {
    select.innerHTML = templateOptionsHTML(saved, { includeCustom });
    select.value = selected;
}