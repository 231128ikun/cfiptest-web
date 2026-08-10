// cloud.js - 云端共享配置状态（设置页 / 导出至云端 / 维护任务同步共用）
import * as api from './api.js';
import { escapeHTML } from './columns.js';

let configs = [];
let channels = [];

/** 拉取云端配置与渠道列表并缓存；返回 { configs, channels }。 */
export async function refreshCloudConfigs() {
    const data = await api.fetchCloudConfigs();
    configs = data.configs || [];
    channels = data.channels || [];
    return { configs, channels };
}

export function cloudConfigs() { return configs; }
export function cloudChannels() { return channels; }

export function channelLabel(channel) {
    const info = channels.find(c => c.id === channel);
    return info ? info.name : (channel || '未知渠道');
}

export function configLabel(id) {
    const cfg = configs.find(c => c.id === id);
    if (!cfg) return id;
    return cfg.name + ' · ' + channelLabel(cfg.channel) + ' · ' + cfg.baseUrl;
}

/** 用配置填充下拉框；selectedId 非空时选中它。 */
export function fillCloudSelect(select, selectedId = '') {
    const before = select.value;
    select.innerHTML = '<option value="">请选择云端配置…</option>'
        + configs.map(c => '<option value="' + escapeHTML(c.id) + '">' + escapeHTML(c.name) + ' · ' + escapeHTML(channelLabel(c.channel)) + ' · ' + escapeHTML(c.baseUrl) + '</option>').join('');
    if (selectedId && configs.some(c => c.id === selectedId)) select.value = selectedId;
    else if (before && configs.some(c => c.id === before)) select.value = before;
}

/** 刷新并填充一个或多个下拉框。 */
export async function loadCloudConfigsInto(...selects) {
    try {
        await refreshCloudConfigs();
    } catch { /* 后端不可用时保持空列表 */ }
    selects.forEach(sel => { if (sel) fillCloudSelect(sel); });
    return { configs, channels };
}
