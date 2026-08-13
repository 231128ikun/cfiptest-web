// tasks.js —— 自动维护页：任务卡片网格 + 编辑弹窗（规则编辑器）+ 一键维护
import { escapeHTML } from './columns.js';
import { loadSavedTemplates, fetchSettingsTemplates as fetchSettingsTpls, persistTemplates, templateOptionFor, templateContentFor, renderTemplateSelect } from './templates.js';
import * as api from './api.js';
import { fillCloudSelect, loadCloudConfigsInto, cloudConfigs } from './cloud.js';
import { formatSpeedMbps, mbpsToSpeedKBs } from './speed-units.js';

const $ = id => document.getElementById(id);
const FIELD_OPTIONS = [
    { value: 'country', label: '国家' },
    { value: 'city', label: '城市' },
    { value: 'port', label: '端口' },
    { value: 'dataCenter', label: '数据中心' },
    { value: 'asn', label: 'ASN' },
    { value: 'region', label: '区域' },
];
const FIELD_LABEL = Object.fromEntries(FIELD_OPTIONS.map(f => [f.value, f.label]));
const WEEKDAY_LABELS = ['周日', '周一', '周二', '周三', '周四', '周五', '周六', '周日'];

function describeCron(expression) {
    const fields = expression.trim().split(/\s+/);
    if (fields.length !== 5) return { valid: false, text: '需要 5 段：分 时 日 月 周' };
    const [minute, hour, day, month, weekday] = fields;
    const number = value => /^\d+$/.test(value) ? Number(value) : null;
    const mm = number(minute);
    const hh = number(hour);
    if (mm !== null && (mm < 0 || mm > 59)) return { valid: false, text: '分钟应在 0–59 之间' };
    if (hh !== null && (hh < 0 || hh > 23)) return { valid: false, text: '小时应在 0–23 之间' };
    const pad = value => String(value).padStart(2, '0');
    if (minute === '*' && hour === '*' && day === '*' && month === '*' && weekday === '*') return { valid: true, text: '每分钟执行' };
    const minuteStep = minute.match(/^\*\/(\d+)$/);
    if (minuteStep && hour === '*' && day === '*' && month === '*' && weekday === '*') return { valid: true, text: `每 ${Number(minuteStep[1])} 分钟执行` };
    const hourStep = hour.match(/^\*\/(\d+)$/);
    if (mm !== null && hourStep && day === '*' && month === '*' && weekday === '*') return { valid: true, text: `每 ${Number(hourStep[1])} 小时的 ${pad(mm)} 分执行` };
    if (mm !== null && hh !== null && day === '*' && month === '*' && weekday === '*') return { valid: true, text: `每天 ${pad(hh)}:${pad(mm)} 执行` };
    const week = number(weekday);
    if (mm !== null && hh !== null && day === '*' && month === '*' && week !== null && week >= 0 && week <= 7) return { valid: true, text: `每${WEEKDAY_LABELS[week]} ${pad(hh)}:${pad(mm)} 执行` };
    const monthDay = number(day);
    if (mm !== null && hh !== null && monthDay !== null && monthDay >= 1 && monthDay <= 31 && month === '*' && weekday === '*') return { valid: true, text: `每月 ${monthDay} 日 ${pad(hh)}:${pad(mm)} 执行` };
    const token = /^(\*|\d+)(?:-\d+)?(?:\/\d+)?(?:,(?:\*|\d+)(?:-\d+)?(?:\/\d+)?)*$/;
    if (!fields.every(field => token.test(field))) return { valid: false, text: '格式无效，仅支持 *、数字、范围、列表和步长' };
    return { valid: true, text: '按此 Cron 计划执行（保存时会进一步校验范围）' };
}

function scheduleLabel(schedule) {
    if (!schedule?.enabled) return '未设置定时';
    return describeCron(schedule.cron || '').text;
}


export function initTasks({ toast }) {
    function setCapabilities(capabilities = {}) {
        const canPickDirectory = capabilities.pickDirectory !== false;
        const button = $('btn-task-output-pick');
        if (button) button.hidden = !canPickDirectory;
        const hint = document.querySelector('.task-output-dir-hint');
        if (hint && !canPickDirectory) {
            hint.textContent = '留空时自动保存到应用数据目录；移动端不支持选择任意系统文件夹。';
        }
    }
    const state = {
        tasks: [],
        libraries: [],
        libNames: {},
        selected: -1, // 当前编辑的任务下标；-1 = 未选择
        draft: null, // 新建草稿（保存前暂存于此，可立即交互）
        savedTemplates: [],
        running: false,
        runQueue: [],
        pendingEnabled: new Set(),
        officialPorts: { https: [443, 2053, 2083, 2087, 2096, 8443], http: [80, 8080, 8880, 2052, 2082, 2086, 2095] },
    };

    function currentTask() {
        if (state.draft) return state.draft;
        return state.selected >= 0 && state.selected < state.tasks.length ? state.tasks[state.selected] : null;
    }

    function setRunning(running) {
        state.running = running;
        const btn = $('btn-run-all');
        btn.classList.toggle('danger', running);
        btn.textContent = running ? '停止维护' : '一键维护';
    }

    // ---- 加载 ----
    async function loadAll() {
        try {
            const [tasksData, libsData, rangesData] = await Promise.all([
                api.fetchTasks(),
                api.fetchLibraries(),
                api.fetchOfficialRanges(1).catch(() => null),
            ]);
            loadCloudConfigsInto($('task-cloud-select')).then(() => updateCloudOpenButton()).catch(() => {});
            state.tasks = tasksData.tasks || [];
            state.libraries = libsData.libraries || [];
            if (rangesData?.ports) state.officialPorts = rangesData.ports;
            state.libNames = {};
            (state.libraries || []).forEach(l => { state.libNames[l.id] = l.name; });
            if (state.selected >= state.tasks.length) state.selected = -1;
            renderTaskGrid();
            renderLibraryOptions();
        } catch (error) {
            toast(`加载失败：${error.message}`);
        }
    }

    // ---- 卡片网格 ----
    function renderTaskGrid() {
        const wrap = $('task-grid');
        if (state.tasks.length === 0) {
            wrap.innerHTML = '<div class="task-detail-empty">暂无任务，点击右上角「+ 新建任务」创建第一个维护任务</div>';
            return;
        }
        wrap.innerHTML = state.tasks.map((task, i) => {
            const ruleSummary = (task.rules || []).map(r => {
                const conds = (r.conditions || []).map(c => {
                    const vals = (c.values || []).join(',');
                    return `${FIELD_LABEL[c.field] || c.field}${vals ? ':' + vals : '·任意'}`;
                }).join(' ∩ ');
                return conds || '任意';
            }).join('；');
            const limit = task.limit ? `总数 ≤ ${task.limit}` : '不限总数';
            const speed = task.speedEnabled ? '启用测速' : '仅延迟筛选';
            const libraryName = task.librarySource === 'official'
                ? `官方 IP 段（${task.libraryFamily || 'ipv4'}）`
                : task.librarySource === 'remote'
                    ? '远程 URL 库'
                    : (state.libNames[task.libraryId] || task.libraryId || '默认库');
            const cloudMode = Boolean(task.output?.cloud);
            let outputHtml;
            if (cloudMode) {
                const cloudLabel = task.output?.cloudKey || '输出文件名（自动）';
                let cloudHref = '';
                const cfg = cloudConfigs().find(c => c.id === task.output?.cloud);
                const key = (task.output?.cloudKey || '').trim().replace(/^\/+|\/+$/g, '');
                if (cfg && key) {
                    cloudHref = cfg.baseUrl.replace(/\/+$/, '') + '/' + key.split('/').map(encodeURIComponent).join('/');
                }
                outputHtml = '☁ 云端 · ' + escapeHTML(cloudLabel)
                    + (cloudHref ? ' <a class="task-cloud-link" href="' + escapeHTML(cloudHref) + '" target="_blank" rel="noopener" title="打开云端链接">打开 ↗</a>' : '');
            } else if (task.output?.path) {
                const displayPath = isServerAbsolutePath(task.output.path)
                    ? task.output.path.replace(/\\/g, '/') : 'data/' + task.output.path;
                outputHtml = `<a class="task-output-download" href="${escapeHTML(api.autoOutputUrl(task.output.path))}" title="下载输出文件">${escapeHTML(displayPath)}</a>`;
            } else {
                outputHtml = '保存后自动生成（本地）';
            }
            const outputSort = ({ latencyAsc: '延迟升序', latencyDesc: '延迟降序', speedDesc: '速度降序', speedAsc: '速度升序', ipAsc: 'IP 升序', countryAsc: '国家/地区升序' })[task.output?.sort] || '延迟升序';
            const schedule = scheduleLabel(task.schedule);
            const pendingKey = task.id || String(i);
            const isPending = state.pendingEnabled.has(pendingKey);
            return `<article class="task-card ${task.enabled ? 'is-enabled' : 'is-disabled'}" data-i="${i}">
                <div class="task-card-head">
                    <div class="task-card-title">
                        <span class="task-card-kicker">维护任务 ${i + 1}</span>
                        <span class="task-card-name">${escapeHTML(task.name || '未命名')}</span>
                    </div>
                    <div class="task-card-state">
                        <span class="task-badge ${task.enabled ? 'ok' : 'none'}">${task.enabled ? '已启用' : '已停用'}</span>
                        <label class="toggle task-card-toggle" title="控制此任务是否参与一键维护">
                            <span class="toggle-label">启动</span>
                            <input type="checkbox" class="task-enable" data-i="${i}" aria-label="${task.enabled ? '停用' : '启用'}任务 ${escapeHTML(task.name || '未命名')}" ${task.enabled ? 'checked' : ''} ${isPending ? 'disabled' : ''}>
                            <span class="toggle-track"></span>
                        </label>
                    </div>
                </div>
                <div class="task-card-summary">
                    <div class="task-meta-row"><span class="task-meta-label">规则</span><span class="task-meta-value">${escapeHTML(ruleSummary || '任意')}</span></div>
                    <div class="task-meta-row"><span class="task-meta-label">维护来源</span><span class="task-meta-value">${escapeHTML(libraryName)}</span></div>
                    <div class="task-meta-row"><span class="task-meta-label">输出</span><span class="task-meta-value task-output-path">${outputHtml}</span></div>
                    <div class="task-card-flags"><span>${limit}</span><span>${outputSort}</span><span>${speed}</span><span class="${task.schedule?.enabled ? 'is-scheduled' : ''}">${escapeHTML(schedule)}</span>${task.output?.cloud ? '<span class="task-cloud-badge" title="输出文件将同步到云端">☁ 云同步</span>' : ''}</div>
                </div>
                <div class="task-card-actions">
                    <button type="button" class="small task-edit" data-i="${i}">编辑配置</button>
                    <button type="button" class="small primary task-run-one" data-i="${i}">立即维护</button>
                    <button type="button" class="small danger task-del" data-i="${i}">删除</button>
                </div>
            </article>`;
        }).join('');
    }
    function renderLibraryOptions() {
        const sel = $('task-library');
        sel.innerHTML = (state.libraries || []).map(l => `<option value="${escapeHTML(l.id)}">${escapeHTML(l.name)}</option>`).join('');
    }

    // ---- 模板（与工作台导出面板共用 templates.js）----
    function loadTemplates() {
        state.savedTemplates = loadSavedTemplates();
    }

    async function fetchSettingsTemplates() {
        const list = await fetchSettingsTpls();
        if (list) state.savedTemplates = list;
    }

    function tplOptionFor(template) {
        return templateOptionFor(template, state.savedTemplates);
    }

    function renderTplSelect(selected = '') {
        renderTemplateSelect($('task-tpl-select'), state.savedTemplates, selected, { includeCustom: true });
    }

    function tplFor(optionValue) {
        return templateContentFor(optionValue, state.savedTemplates);
    }

    function updateTplCustomVisibility() {
        $('task-tpl-custom').hidden = $('task-tpl-select').value !== 'custom';
        $('task-tpl-save').hidden = $('task-tpl-select').value !== 'custom';
    }

    function updateTaskSampleHint() {
        const el = $('task-sample-n-hint');
        if (!el) return;
        const n = Math.min(256, Math.max(1, parseInt($('task-library-sample-n').value, 10) || 1));
        el.textContent = $('task-library-family').value === 'ipv6'
            ? `IPv6 无法穷举全部地址（每个 /64 子网有 2^64 个地址），按每个 /64 子网抽样，N 最大 256、最多覆盖 1024 个子网；当前 N=${n}。`
            : `每个 /24 网段抽样 N 个，N 最大 256（即该网段全部地址）；当前 N=${n}。`;
    }

    async function saveCurrentTemplate() {
        const tpl = $('task-tpl-custom').value.trim();
        if (!tpl) { toast('模板为空'); return; }
        const name = prompt('模板名称（与导出页「我的模板」共用）', `我的模板 ${state.savedTemplates.length + 1}`);
        if (!name) return;
        state.savedTemplates = [...state.savedTemplates.filter(t => t.template !== tpl), { name: name.trim(), template: tpl }];
        try {
            await persistTemplates(state.savedTemplates);
        } catch (error) {
            toast(`保存模板失败：${error.message}`);
            return;
        }
        renderTplSelect(`saved:${state.savedTemplates.length - 1}`);
        $('task-tpl-custom').value = tpl;
        updateTplCustomVisibility();
        toast('模板已保存');
    }

    // ---- 编辑弹窗 ----
    function openEditor(task) {
        state.draft = state.tasks.includes(task) ? null : task;
        state.selected = state.tasks.indexOf(task);
        $('task-editor-overlay').hidden = false;
        fillForm(task);
    }

    function closeEditor() {
        $('task-editor-overlay').hidden = true;
        state.selected = -1;
        state.draft = null;
        renderTaskGrid();
    }

    function updateTaskLibraryPorts(preferredPort) {
        const protocol = $('task-library-protocol').value === 'http' ? 'http' : 'https';
        const ports = state.officialPorts[protocol] || (protocol === 'http' ? [80] : [443]);
        const selected = Number(preferredPort || $('task-library-port').value);
        $('task-library-port').innerHTML = ports.map(port => `<option value="${port}">${port}</option>`).join('');
        $('task-library-port').value = ports.includes(selected) ? String(selected) : String(ports[0]);
    }
    function updateTaskSourceUI() {
        const source = $('task-library-source').value || 'official';
        document.querySelectorAll('[data-task-source]').forEach(panel => {
            panel.hidden = panel.dataset.taskSource !== source;
        });
        const initMode = $('task-input-mode').value || 'none';
        document.querySelectorAll('[data-task-init]').forEach(panel => {
            panel.hidden = panel.dataset.taskInit !== initMode;
        });
    }
    function updateTaskExportUI() {
        const mode = $('task-export-mode').value || 'local';
        document.querySelectorAll('[data-export-mode]').forEach(panel => {
            panel.hidden = panel.dataset.exportMode !== mode;
        });
        updateCloudOpenButton();
    }
    function cloudLinkFor() {
        const cfg = cloudConfigs().find(c => c.id === $('task-cloud-select').value);
        if (!cfg) return { url: '', reason: '请先选择云端配置' };
        const key = $('task-cloud-key').value.trim().replace(/^\/+|\/+$/g, '');
        if (!key) return { url: '', reason: '留空时自动使用输出文件名，无法预知链接' };
        return { url: cfg.baseUrl.replace(/\/+$/, '') + '/' + key.split('/').map(encodeURIComponent).join('/'), reason: '' };
    }
    function updateCloudOpenButton() {
        const btn = $('btn-task-cloud-open');
        const { url, reason } = cloudLinkFor();
        btn.disabled = !url;
        btn.title = url ? '打开 ' + url : reason;
    }
    function openCloudLink() {
        const { url, reason } = cloudLinkFor();
        if (!url) { toast(reason); return; }
        window.open(url, '_blank', 'noopener');
    }
    function isServerAbsolutePath(p) {
        p = (p || '').trim();
        return /^[a-zA-Z]:[\\/]/.test(p) || p.startsWith('/') || p.startsWith('\\\\');
    }

    function updateTaskInitFileStatus() {
        const status = $('task-init-file-status');
        const raw = $('task-init-file-path').value.trim();
        if (isServerAbsolutePath(raw)) {
            status.textContent = `服务器将直接读取：${raw}（文件需已存在于运行 iptest-web 的主机上）`;
            status.classList.remove('error');
            return;
        }
        if (raw) {
            status.textContent = `已保存：${raw}（data 目录内相对路径）`;
            status.classList.remove('error');
            return;
        }
        status.textContent = '可选：先用本地 TXT/CSV（选择并导入）或服务器已有文件，把基础 IP 导入 IP 库再开始维护。';
        status.classList.remove('error');
    }


    function fillForm(task) {
        $('task-name').value = task.name || '';
        if (state.libraries.some(l => l.id === task.libraryId)) $('task-library').value = task.libraryId;
        const source = task.librarySource || (task.input?.mode === 'official' ? 'official' : 'local');
        $('task-library-source').value = ['official', 'local', 'remote'].includes(source) ? source : 'local';
        $('task-library-url').value = task.libraryUrl || '';
        $('task-library-family').value = task.libraryFamily === 'ipv6' ? 'ipv6' : 'ipv4';
        updateTaskSampleHint();
        $('task-library-sample-n').value = Number(task.librarySampleN) > 0 ? task.librarySampleN : 1;
        $('task-library-protocol').value = task.libraryProtocol === 'http' ? 'http' : 'https';
        updateTaskLibraryPorts(task.libraryPort);
        const input = task.input || { mode: 'none' };
        $('task-input-mode').value = ['none', 'file', 'remote'].includes(input.mode) ? input.mode : 'none';
        $('task-init-file-path').value = (input.file || '').replace(/\\/g, '/');
        $('task-init-url').value = input.url || '';
        updateTaskSourceUI();
        updateTaskInitFileStatus();
        fillCloudSelect($('task-cloud-select'), task.output?.cloud || '');
        $('task-cloud-key').value = task.output?.cloudKey || '';
        $('task-output').value = task.output?.path || '';
        $('task-export-mode').value = task.output?.cloud ? 'cloud' : 'local';
        updateTaskExportUI();
        $('task-format').value = task.output?.format === 'csv' ? 'csv' : 'txt';
        $('task-sort').value = task.output?.sort || 'latencyAsc';
        $('task-limit').value = task.limit > 0 ? task.limit : 200;
        $('task-speed').checked = Boolean(task.speedEnabled);
        $('task-schedule-enabled').checked = Boolean(task.schedule?.enabled);
        $('task-schedule-cron').value = task.schedule?.cron || '0 3 * * *';
        updateScheduleUI();
        const tpl = task.output?.template || '{ip}:{port}#{emoji}{country}';
        $('task-tpl-custom').value = tpl;
        renderTplSelect(tplOptionFor(tpl));
        updateTplCustomVisibility();
        renderRules(task.rules || []);
        $('task-form-status').textContent = '';
    }

    function renderRules(rules) {
        const wrap = $('task-rules');
        if (!rules.length) {
            wrap.innerHTML = '<div class="auto-groups-empty">暂无规则，点击「+ 添加规则」</div>';
            return;
        }
        wrap.innerHTML = rules.map((rule, ri) => `
            <div class="task-rule" data-ri="${ri}">
                <div class="task-rule-head">
                    <strong>${escapeHTML(rule.name || `规则 ${ri + 1}`)}</strong>
                    <button type="button" class="small task-rule-remove" data-ri="${ri}">删除规则</button>
                </div>
                <div class="task-rule-conditions">
                    ${(rule.conditions || []).map((c, ci) => `
                        <div class="task-condition" data-ri="${ri}" data-ci="${ci}">
                            <select class="c-field" data-ri="${ri}" data-ci="${ci}">
                                ${FIELD_OPTIONS.map(f => `<option value="${f.value}" ${c.field === f.value ? 'selected' : ''}>${f.label}</option>`).join('')}
                            </select>
                            <input class="c-values" data-ri="${ri}" data-ci="${ci}" value="${escapeHTML((c.values || []).join(','))}" placeholder="${c.field === 'country' || c.field === 'dataCenter' ? '多值逗号分隔；留空 = 任意' : '多值逗号分隔'}">
                            <button type="button" class="small c-remove" data-ri="${ri}" data-ci="${ci}" title="删除条件">✕</button>
                        </div>`).join('')}
                    <button type="button" class="small c-add" data-ri="${ri}">+ 添加条件</button>
                </div>
                <div class="task-rule-params">
                    <label>每个组合取前 <input type="number" min="0" class="r-limit" data-ri="${ri}" value="${rule.limit || ''}" placeholder="0=不限"> 条</label>
                    <label>延迟 <input type="number" min="0" class="r-lat-min" data-ri="${ri}" value="${rule.latencyMin || ''}" placeholder="min"> ~ <input type="number" min="0" class="r-lat-max" data-ri="${ri}" value="${rule.latencyMax || ''}" placeholder="max"> ms</label>
                    <label title="按常见网络带宽单位填写；旧任务会自动换算">速度 <input type="number" min="0" step="0.000001" class="r-spd-min" data-ri="${ri}" value="${formatSpeedMbps(rule.speedMin)}" placeholder="min" ${$('task-speed').checked ? '' : 'disabled'}> ~ <input type="number" min="0" step="0.000001" class="r-spd-max" data-ri="${ri}" value="${formatSpeedMbps(rule.speedMax)}" placeholder="max" ${$('task-speed').checked ? '' : 'disabled'}> Mbps</label>
                </div>
            </div>`).join('');
    }

    function collectForm() {
        const task = currentTask() || { rules: [] };
        task.name = $('task-name').value.trim();
        if (task.enabled === undefined) task.enabled = true;
        const source = $('task-library-source').value || 'local';
        task.librarySource = source;
        task.libraryId = $('task-library').value || 'default';
        task.libraryUrl = source === 'remote' ? $('task-library-url').value.trim() || undefined : undefined;
        task.libraryFamily = undefined;
        task.librarySampleMode = undefined;
        task.librarySampleN = undefined;
        task.libraryProtocol = undefined;
        task.libraryPort = undefined;
        if (source === 'official') {
            task.libraryFamily = $('task-library-family').value;
            task.librarySampleMode = 'n';
            task.librarySampleN = Number($('task-library-sample-n').value) || 1;
            task.libraryProtocol = $('task-library-protocol').value;
            task.libraryPort = Number($('task-library-port').value) || undefined;
        }
        const initMode = $('task-input-mode').value || 'none';
        if (initMode === 'file') {
            task.input = { mode: 'file', file: $('task-init-file-path').value.trim() || undefined };
        } else if (initMode === 'remote') {
            task.input = { mode: 'remote', url: $('task-init-url').value.trim() || undefined };
        } else {
            task.input = { mode: 'none' };
        }
        const exportMode = $('task-export-mode').value || 'local';
        task.output = {
            format: $('task-format').value,
            template: $('task-tpl-custom').value.trim(),
            sort: $('task-sort').value,
        };
        if (exportMode === 'cloud') {
            task.output.cloud = $('task-cloud-select').value;
            task.output.cloudKey = $('task-cloud-key').value.trim() || undefined;
            delete task.output.path;
        } else {
            task.output.path = $('task-output').value.trim() || undefined;
            delete task.output.cloud;
            delete task.output.cloudKey;
        }
        const limitRaw = $('task-limit').value.trim();
        task.limit = limitRaw === '' ? 200 : Math.max(0, Number(limitRaw) || 0);
        task.speedEnabled = $('task-speed').checked;
        // 检测参数统一走设置页全局默认；旧任务残留的任务级覆盖一并清除。
        delete task.latencyConcurrency;
        delete task.latencyTimeoutMs;
        delete task.latencyProbes;
        delete task.latencyHTTPProbes;
        delete task.speedConcurrency;
        delete task.speedDurationSec;
        task.schedule = {
            enabled: $('task-schedule-enabled').checked,
            cron: $('task-schedule-cron').value.trim() || '0 3 * * *',
        };
        const rules = [...document.querySelectorAll('#task-rules .task-rule')].map((row, ri) => {
            const conditions = [...row.querySelectorAll('.task-condition')].map(cRow => ({
                field: cRow.querySelector('.c-field').value,
                values: (cRow.querySelector('.c-values').value || '')
                    .split(/[,，;；]/).map(s => s.trim()).filter(Boolean),
            })).filter(c => c.values.length > 0);
            const num = sel => { const v = Number(row.querySelector(sel).value); return Number.isFinite(v) && v > 0 ? v : 0; };
            const rule = {
                name: `规则 ${ri + 1}`,
                conditions,
                limit: num('.r-limit'),
                latencyMin: num('.r-lat-min'),
                latencyMax: num('.r-lat-max'),
                speedMin: task.speedEnabled ? mbpsToSpeedKBs(num('.r-spd-min')) : 0,
                speedMax: task.speedEnabled ? mbpsToSpeedKBs(num('.r-spd-max')) : 0,
            };
            rule.conditions.forEach(c => {
                if (c.field === 'country' || c.field === 'dataCenter') c.values = c.values.map(v => v.toUpperCase());
            });
            return rule;
        });
        task.rules = rules;
        return task;
    }

    async function saveTask() {
        const task = collectForm();
        if (!task.name) { toast('任务名称不能为空'); return; }
        if (!task.rules.length) { toast('至少需要一条规则'); return; }
        if ($('task-export-mode').value === 'cloud' && !task.output?.cloud) {
            toast('请先在「设置 → 云端存储」添加并选择云端站点');
            return;
        }
        try {
            await api.validateTask(task);
        } catch (error) {
            toast(`校验失败：${error.message}`);
            return;
        }
        if (!task.id) task.id = `t-${Date.now().toString(36)}`;
        if (!(state.selected >= 0 && state.selected < state.tasks.length && state.tasks[state.selected] === task)) {
            state.tasks.push(task);
            state.selected = state.tasks.length - 1;
        }
        try {
            await api.saveTasks(state.tasks);
            toast('任务已保存');
            closeEditor();
        } catch (error) {
            toast(`保存失败：${error.message}`);
        }
    }

    async function deleteTask() {
        const task = currentTask();
        if (!task) return;
        if (state.draft) {
            closeEditor();
            toast('已放弃新建任务');
            return;
        }
        if (!confirm(`确认删除任务「${task.name}」？`)) return;
        state.tasks = state.tasks.filter(t => t !== task);
        try {
            await api.saveTasks(state.tasks);
            toast('任务已删除');
            closeEditor();
        } catch (error) {
            toast(`保存失败：${error.message}`);
        }
    }

    // ---- 运行 ----
    async function runTaskId(taskId) {
        try {
            const result = await api.runAuto(taskId);
            setRunning(true);
            state.runningTaskId = taskId;
        } catch (error) {
            toast(`启动失败：${error.message}`);
        }
    }

    function runAll() {
        if (state.running) { toast('已有任务在运行'); return; }
        const enabled = state.tasks.filter(t => t.enabled && t.id);
        if (!enabled.length) { toast('没有已启用的任务（勾选卡片上的「启用」）'); return; }
        state.runQueue = enabled.map(t => t.id);
        runNextInQueue();
    }

    function runNextInQueue() {
        if (!state.runQueue.length) {
            setRunning(false);
            return;
        }
        runTaskId(state.runQueue.shift());
    }

    function stop() {
        const btn = $('btn-run-all');
        btn.disabled = true;
        btn.textContent = '正在停止…';
        api.stopTask('').then(result => {
            // 返回 false 说明此刻没有活动任务（例如队列切换间隙），
            // 不能等 onDone，直接恢复按钮避免卡在“正在停止…”。
            if (!result || result.stopped !== true) setRunning(state.running);
        }).catch(error => {
            toast(`停止失败：${error.message}`);
            setRunning(state.running);
        });
    }

    function onAuto(message) {
        if (!state.running || !message) return;
        let event;
        try { event = JSON.parse(message); } catch { return; }
        if (event.stage !== 'cloud') return;
        const taskName = event.task ? `任务「${event.task}」` : '维护任务';
        if (event.status === 'uploading') toast(`${taskName}正在上传至云端…`);
        else if (event.status === 'success') toast(`${taskName}云端同步成功`);
        else if (event.status === 'error') toast(`${taskName}云端同步失败：${event.error || '未知错误'}`);
    }

    function onDone(message, reason) {
        if (!state.running) return;
        if (reason === 'stopped') {
            setRunning(false);
            state.runQueue = [];
        }
        toast(message || (reason === 'stopped' ? '维护任务已停止' : '维护任务已完成'));
        loadAll();
        if (state.runQueue.length) {
            setTimeout(runNextInQueue, 600);
        } else {
            setRunning(false);
        }
    }

    async function updateTaskEnabled(index, enabled) {
        const task = state.tasks[index];
        if (!task) return;
        const previous = task.enabled;
        const pendingKey = task.id || String(index);
        task.enabled = enabled;
        state.pendingEnabled.add(pendingKey);
        renderTaskGrid();
        try {
            await api.saveTasks(state.tasks);
        } catch (error) {
            task.enabled = previous;
            toast(`更新任务状态失败：${error.message}`);
        } finally {
            state.pendingEnabled.delete(pendingKey);
            renderTaskGrid();
        }
    }

    // ---- 事件 ----
    $('task-input-mode').addEventListener('change', updateTaskSourceUI);
    $('task-library-source').addEventListener('change', updateTaskSourceUI);
    $('task-library-protocol').addEventListener('change', () => updateTaskLibraryPorts());
    $('task-library-family').addEventListener('change', updateTaskSampleHint);
    $('task-library-sample-n').addEventListener('input', updateTaskSampleHint);
    $('task-init-upload').addEventListener('change', async event => {
        const file = event.target.files?.[0];
        if (!file) return;
        const status = $('task-init-file-status');
        status.textContent = `正在导入 ${file.name}…`;
        try {
            const text = await file.text();
            const result = await api.uploadAutoInput(file.name, text);
            $('task-init-file-path').value = result.path;
            status.textContent = `已保存 ${result.name || file.name}：${result.targets} 个目标，${result.bytes} 字节`;
            toast('本地文件已导入，可用于定时维护');
        } catch (error) {
            status.textContent = `导入失败：${error.message}`;
            toast(error.message);
        } finally {
            event.target.value = '';
        }
    });
    $('task-init-file-path').addEventListener('input', updateTaskInitFileStatus);
    $('btn-task-output-pick').addEventListener('click', async () => {
        const task = currentTask();
        if (!task) return;
        const btn = $('btn-task-output-pick');
        btn.disabled = true;
        try {
            const data = await api.pickOutputDir();
            if (data.path) {
                $('task-output').value = data.path;
                task.output = task.output || {};
                task.output.path = data.path;
                const details = $('task-output').closest('details');
                if (details) details.open = true;
                toast(`已选择输出目录：${data.path}`);
            } else {
                toast('已取消选择');
            }
        } catch (error) {
            toast(`选择失败：${error.message}`);
        } finally {
            btn.disabled = false;
        }
    });
    $('task-grid').addEventListener('change', e => {
        const enable = e.target.closest('.task-enable');
        if (!enable) return;
        updateTaskEnabled(Number(enable.dataset.i), enable.checked);
    });
    $('task-grid').addEventListener('click', e => {
        const edit = e.target.closest('.task-edit');
        if (edit) { openEditor(state.tasks[Number(edit.dataset.i)]); return; }
        const runOne = e.target.closest('.task-run-one');
        if (runOne) {
            const task = state.tasks[Number(runOne.dataset.i)];
            if (!state.running && task.id) runTaskId(task.id);
            return;
        }
        const del = e.target.closest('.task-del');
        if (del) {
            state.selected = Number(del.dataset.i);
            deleteTask();
        }
    });
    $('btn-task-new').addEventListener('click', () => {
        const task = {
            name: '新任务', enabled: true, librarySource: 'local',
            libraryId: state.libraries[0]?.id || 'default',
            libraryFamily: 'ipv4', librarySampleMode: 'n', librarySampleN: 1,
            libraryProtocol: 'https', libraryPort: 443,
            input: { mode: 'none' },
            output: { format: 'txt', template: '{ip}:{port}#{emoji}{country}' },
            limit: 200, speedEnabled: false, schedule: { enabled: false, cron: '0 3 * * *' },
            rules: [{ name: '规则 1', conditions: [{ field: 'country', values: [] }], limit: 0 }],
        };
        openEditor(task);
    });
    $('btn-task-editor-close').addEventListener('click', closeEditor);
    $('task-editor-overlay').addEventListener('click', e => { if (e.target === $('task-editor-overlay')) closeEditor(); });
    $('task-export-mode').addEventListener('change', updateTaskExportUI);
    $('task-cloud-select').addEventListener('change', updateCloudOpenButton);
    $('task-cloud-key').addEventListener('input', updateCloudOpenButton);
    $('btn-task-cloud-open').addEventListener('click', openCloudLink);
    $('task-save').addEventListener('click', saveTask);
    $('task-delete').addEventListener('click', deleteTask);
    $('btn-run-all').addEventListener('click', () => {
        if (state.running) stop();
        else runAll();
    });
    function updateScheduleUI() {
        const enabled = $('task-schedule-enabled').checked;
        $('task-schedule-settings').hidden = !enabled;
        const described = describeCron($('task-schedule-cron').value);
        const description = $('task-schedule-description');
        description.textContent = described.text;
        description.classList.toggle('error', !described.valid);
        $('task-schedule-cron').setAttribute('aria-invalid', String(!described.valid));
    }
    $('task-speed').addEventListener('change', () => { const t = currentTask(); if (t) renderRules(t.rules || []); });
    $('task-schedule-enabled').addEventListener('change', updateScheduleUI);
    $('task-schedule-cron').addEventListener('input', updateScheduleUI);
    document.querySelectorAll('.task-cron-preset').forEach(button => button.addEventListener('click', () => {
        $('task-schedule-cron').value = button.dataset.cron || '0 3 * * *';
        updateScheduleUI();
        $('task-schedule-cron').focus();
    }));
    $('task-tpl-select').addEventListener('change', e => { const tpl = tplFor(e.target.value); if (tpl) $('task-tpl-custom').value = tpl; updateTplCustomVisibility(); });
    $('task-tpl-custom').addEventListener('input', () => { if ($('task-tpl-select').value !== 'custom') $('task-tpl-select').value = 'custom'; });
    $('task-tpl-save').addEventListener('click', saveCurrentTemplate);
    $('task-rule-add').addEventListener('click', () => {
        const t = currentTask();
        if (!t) return;
        t.rules = t.rules || [];
        t.rules.push({ name: `规则 ${t.rules.length + 1}`, conditions: [], limit: 0 });
        renderRules(t.rules);
    });
    $('task-rules').addEventListener('click', e => {
        const t = currentTask();
        if (!t) return;
        const removeRule = e.target.closest('.task-rule-remove');
        if (removeRule) { t.rules.splice(Number(removeRule.dataset.ri), 1); renderRules(t.rules); return; }
        const removeCond = e.target.closest('.c-remove');
        if (removeCond) { t.rules[Number(removeCond.dataset.ri)].conditions.splice(Number(removeCond.dataset.ci), 1); renderRules(t.rules); return; }
        const addCond = e.target.closest('.c-add');
        if (addCond) {
            const ri = Number(addCond.dataset.ri);
            t.rules[ri].conditions = t.rules[ri].conditions || [];
            t.rules[ri].conditions.push({ field: 'country', values: [] });
            renderRules(t.rules);
        }
    });
    $('task-rules').addEventListener('input', e => {
        const t = currentTask();
        if (!t) return;
        const row = e.target.closest('.task-rule');
        if (!row) return;
        const ri = Number(row.dataset.ri);
        const rule = t.rules[ri];
        if (!rule) return;
        const cls = e.target.classList;
        if (cls.contains('r-limit')) rule.limit = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (cls.contains('r-lat-min')) rule.latencyMin = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (cls.contains('r-lat-max')) rule.latencyMax = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (cls.contains('r-spd-min')) rule.speedMin = mbpsToSpeedKBs(e.target.value);
        else if (cls.contains('r-spd-max')) rule.speedMax = mbpsToSpeedKBs(e.target.value);
        else if (cls.contains('c-field')) {
            const ci = Number(e.target.dataset.ci);
            rule.conditions[ci].field = e.target.value;
            // 更新占位符提示
            const input = e.target.closest('.task-condition').querySelector('.c-values');
            input.placeholder = (e.target.value === 'country' || e.target.value === 'dataCenter') ? '多值逗号/分号分隔；留空 = 任意' : '多值逗号/分号分隔';
        } else if (cls.contains('c-values')) {
            const ci = Number(e.target.dataset.ci);
            rule.conditions[ci].values = (e.target.value || '').split(/[,，;；]/).map(s => s.trim()).filter(Boolean);
        }
    });

    // 库变更实时刷新（新建/改名/删除后任务页下拉同步）
    window.addEventListener('library-changed', () => {
        api.fetchLibraries().then(data => {
            state.libraries = data.libraries || [];
            state.libNames = {};
            (state.libraries || []).forEach(l => { state.libNames[l.id] = l.name; });
            renderLibraryOptions();
            renderTaskGrid();
        }).catch(() => {});
    });

    loadTemplates();
    fetchSettingsTemplates();
    loadAll();

    return { onAuto, onDone, isAutoRunning: () => state.running, refreshLibrary: () => {}, setCapabilities };
}