// tasks.js —— 自动维护页：任务卡片网格 + 编辑弹窗（规则编辑器）+ 一键维护
import { escapeHTML } from './columns.js';
import { loadSavedTemplates, fetchSettingsTemplates as fetchSettingsTpls, persistTemplates, templateOptionFor, templateContentFor, renderTemplateSelect } from './templates.js';
import * as api from './api.js';

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
        pathBrowser: null,
    };

    function currentTask() {
        if (state.draft) return state.draft;
        return state.selected >= 0 && state.selected < state.tasks.length ? state.tasks[state.selected] : null;
    }

    function setRunning(running) {
        state.running = running;
        $('btn-run-all').disabled = running;
    }

    // ---- 加载 ----
    async function loadAll() {
        try {
            const [tasksData, libsData, rangesData] = await Promise.all([
                api.fetchTasks(),
                api.fetchLibraries(),
                api.fetchOfficialRanges(1).catch(() => null),
            ]);
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
            const outputPath = task.output?.path
                ? (isServerAbsolutePath(task.output.path) ? task.output.path.replace(/\\/g, '/') : 'data/' + task.output.path)
                : '保存后自动生成';
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
                    <div class="task-meta-row"><span class="task-meta-label">输出</span><span class="task-meta-value task-output-path">${escapeHTML(outputPath)}</span></div>
                    <div class="task-card-flags"><span>${limit}</span><span>${outputSort}</span><span>${speed}</span><span class="${task.schedule?.enabled ? 'is-scheduled' : ''}">${escapeHTML(schedule)}</span></div>
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

    // ---- 服务器路径浏览器（选择初始化文件 / 输出位置）----
    function joinServerPath(dir, name) {
        return String(dir || '').replace(/[\\/]+$/, '') + '/' + name;
    }

    async function openPathBrowser(mode = 'file') {
        const s = state.pathBrowser = state.pathBrowser || { current: '', parent: '', home: '', dataDir: '', selected: null, entries: [], mode: 'file' };
        s.mode = mode === 'output' ? 'output' : 'file';
        if (s.mode === 'output') {
            const raw = $('task-output').value.trim().replace(/\\/g, '/');
            // 输出模式：已有绝对路径时定位到其所在目录，方便直接确认或改文件名。
            s.current = isServerAbsolutePath(raw) ? (raw.replace(/[^/]+$/, '') || '/') : (s.dataDir || '');
        } else {
            s.current = s.current || s.dataDir || '';
        }
        pathBrowserModeUI();
        $('path-browser-overlay').hidden = false;
        await browseServerPath(s.current || s.dataDir || '');
    }

    function pathBrowserModeUI() {
        const s = state.pathBrowser;
        if (!s) return;
        const isOutput = s.mode === 'output';
        $('path-browser-title').textContent = isOutput ? '选择服务器上的输出位置' : '选择服务器上的初始化文件';
        $('path-browser-pick').textContent = isOutput ? '选择此路径' : '选择此文件';
        $('path-browser-current').placeholder = isOutput ? '可编辑：目录 + 文件名，回车确认' : '服务器路径';
    }

    function closePathBrowser() {
        $('path-browser-overlay').hidden = true;
    }

    async function browseServerPath(dir) {
        const s = state.pathBrowser;
        if (!s) return;
        const list = $('path-browser-list');
        const status = $('path-browser-status');
        list.innerHTML = '<div class="path-browser-empty">加载中…</div>';
        status.textContent = '';
        status.classList.remove('error');
        $('path-browser-pick').disabled = true;
        s.selected = null;
        try {
            const data = await api.browseAutoPaths(dir);
            s.current = data.current || '';
            s.parent = data.parent || '';
            s.home = data.home || '';
            s.dataDir = data.dataDir || '';
            s.entries = data.entries || [];
            $('path-browser-current').value = s.current;
            $('path-browser-parent').disabled = !s.parent;
            $('path-browser-home').hidden = !s.home;
            renderPathBrowserList(data.error || '');
        } catch (error) {
            status.textContent = `浏览失败：${error.message}`;
            status.classList.add('error');
            list.innerHTML = '<div class="path-browser-empty">浏览失败</div>';
        }
    }

    function formatPathBrowserSize(size) {
        if (!Number.isFinite(size) || size < 0) return '';
        if (size < 1024) return `${size} B`;
        if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
        if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`;
        return `${(size / 1024 / 1024 / 1024).toFixed(1)} GB`;
    }

    function renderPathBrowserList(message) {
        const s = state.pathBrowser;
        if (!s) return;
        const list = $('path-browser-list');
        const status = $('path-browser-status');
        status.classList.remove('error');
        if (message) {
            status.textContent = message;
            status.classList.add('error');
            list.innerHTML = '<div class="path-browser-empty">无法打开该目录</div>';
            return;
        }
        const entries = s.entries || [];
        status.textContent = entries.length
            ? s.mode === 'output'
                ? `${entries.length} 项 · 双击目录进入，单击文件或编辑路径框后点「选择此路径」`
                : `${entries.length} 项 · 单击文件选中，双击目录进入`
            : '空目录';
        if (!entries.length) {
            list.innerHTML = '<div class="path-browser-empty">空目录</div>';
            return;
        }
        list.innerHTML = entries.map(e => `
            <button type="button" class="path-browser-item ${e.isDir ? 'is-dir' : 'is-file'}" data-name="${escapeHTML(e.name)}" ${e.isDir ? 'data-dir="1"' : ''}>
                <span class="path-browser-icon">${e.isDir ? '📁' : '📄'}</span>
                <span class="path-browser-name">${escapeHTML(e.name)}</span>
                ${e.isDir ? '' : `<span class="path-browser-size">${formatPathBrowserSize(e.size)}</span>`}
            </button>`).join('');
    }

    function markPathBrowserSelected(item, path) {
        const s = state.pathBrowser;
        if (!s || !path) return;
        s.selected = path;
        document.querySelectorAll('#path-browser-list .is-selected').forEach(el => el.classList.remove('is-selected'));
        if (item) item.classList.add('is-selected');
        if (s.mode === 'output') $('path-browser-current').value = path;
        $('path-browser-pick').disabled = false;
        $('path-browser-status').textContent = s.mode === 'output'
            ? `已选择：${path}（可继续编辑文件名）`
            : `已选择：${path}`;
    }

    function onPathBrowserListClick(e) {
        const item = e.target.closest('.path-browser-item');
        if (!item || !state.pathBrowser) return;
        const s = state.pathBrowser;
        if (s.mode === 'output') {
            // 输出模式：单击文件回填路径；目录仅双击进入。
            if (item.classList.contains('is-dir')) return;
            markPathBrowserSelected(item, joinServerPath(s.current, item.dataset.name));
            return;
        }
        if (item.classList.contains('is-dir')) {
            s.selected = null;
            $('path-browser-pick').disabled = true;
            return;
        }
        markPathBrowserSelected(item, joinServerPath(s.current, item.dataset.name));
    }

    function onPathBrowserListDblClick(e) {
        const item = e.target.closest('.path-browser-item[data-dir="1"]');
        if (item && state.pathBrowser) {
            browseServerPath(joinServerPath(state.pathBrowser.current, item.dataset.name));
        }
    }

    function onPathBrowserCurrentInput() {
        const s = state.pathBrowser;
        if (!s || s.mode !== 'output') return;
        const v = $('path-browser-current').value.trim();
        s.selected = v || null;
        $('path-browser-pick').disabled = !v;
        document.querySelectorAll('#path-browser-list .is-selected').forEach(el => el.classList.remove('is-selected'));
    }

    function onPathBrowserCurrentKeydown(e) {
        if (e.key !== 'Enter' || !state.pathBrowser) return;
        const v = $('path-browser-current').value.trim();
        if (state.pathBrowser.mode === 'output') {
            if (v) { state.pathBrowser.selected = v; confirmPathBrowserPick(); }
            return;
        }
        if (v) browseServerPath(v);
    }

    function confirmPathBrowserPick() {
        const s = state.pathBrowser;
        if (!s?.selected) return;
        if (s.mode === 'output') {
            $('task-output').value = s.selected.replace(/\\/g, '/');
            updateTaskOutputPreview();
            closePathBrowser();
            return;
        }
        $('task-init-file-path').value = s.selected;
        $('task-init-file-status').textContent = `已选择服务器文件：${s.selected}；定时维护将直接读取该路径。`;
        $('task-init-file-status').classList.remove('error');
        closePathBrowser();
    }
    function updateTaskOutputPreview() {
        const preview = $('task-output-preview');
        const format = $('task-format').value === 'csv' ? 'csv' : 'txt';
        let raw = $('task-output').value.trim().replace(/\\/g, '/');
        const taskName = $('task-name').value.trim() || '任务名';
        if (isServerAbsolutePath(raw)) {
            raw = raw.replace(/\.(?:txt|csv)$/i, '');
            preview.textContent = '实际输出：' + raw + '.' + format + '（服务器文件绝对路径；在运行 iptest-web 的主机上写入，非浏览器本机路径）';
            preview.classList.remove('error');
            return;
        }
        if (raw.split('/').includes('..')) {
            preview.textContent = '路径无效：不能使用 ../，只能填写 data 目录内的相对路径或服务器绝对路径';
            preview.classList.add('error');
            return;
        }
        raw = raw.replace(/^\.\//, '').replace(/\.(?:txt|csv)$/i, '');
        const relative = raw ? (raw.includes('/') ? raw : 'out/' + raw) : 'out/' + taskName;
        preview.textContent = '实际输出：data/' + relative + '.' + format + '（服务器 data 目录；不是浏览器本机路径）';
        preview.classList.remove('error');
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
        $('task-output').value = task.output?.path || '';
        $('task-format').value = task.output?.format === 'csv' ? 'csv' : 'txt';
        updateTaskOutputPreview();
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
                    <label>速度 <input type="number" min="0" class="r-spd-min" data-ri="${ri}" value="${rule.speedMin || ''}" placeholder="min" ${$('task-speed').checked ? '' : 'disabled'}> ~ <input type="number" min="0" class="r-spd-max" data-ri="${ri}" value="${rule.speedMax || ''}" placeholder="max" ${$('task-speed').checked ? '' : 'disabled'}> kB/s</label>
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
        task.output = {
            path: $('task-output').value.trim() || undefined,
            format: $('task-format').value,
            template: $('task-tpl-custom').value.trim(),
            sort: $('task-sort').value,
        };
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
                    .split(/[,，]/).map(s => s.trim()).filter(Boolean),
            })).filter(c => c.values.length > 0);
            const num = sel => { const v = Number(row.querySelector(sel).value); return Number.isFinite(v) && v > 0 ? v : 0; };
            const rule = {
                name: `规则 ${ri + 1}`,
                conditions,
                limit: num('.r-limit'),
                latencyMin: num('.r-lat-min'),
                latencyMax: num('.r-lat-max'),
                speedMin: task.speedEnabled ? num('.r-spd-min') : 0,
                speedMax: task.speedEnabled ? num('.r-spd-max') : 0,
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

    function stop() { api.stopTask('').catch(() => {}); }

    function onDone(message, reason) {
        if (!state.running) return;
        if (reason === 'stopped') {
            setRunning(false);
            state.runQueue = [];
        }
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
    $('task-init-browse').addEventListener('click', () => openPathBrowser('file'));
    $('task-output-browse').addEventListener('click', () => openPathBrowser('output'));
    $('btn-path-browser-close').addEventListener('click', closePathBrowser);
    $('path-browser-cancel').addEventListener('click', closePathBrowser);
    $('path-browser-overlay').addEventListener('click', e => { if (e.target === $('path-browser-overlay')) closePathBrowser(); });
    $('path-browser-parent').addEventListener('click', () => browseServerPath(state.pathBrowser?.parent || ''));
    $('path-browser-refresh').addEventListener('click', () => browseServerPath(state.pathBrowser?.current || ''));
    $('path-browser-home').addEventListener('click', () => browseServerPath(state.pathBrowser?.home || ''));
    $('path-browser-data').addEventListener('click', () => browseServerPath(state.pathBrowser?.dataDir || ''));
    $('path-browser-current').addEventListener('input', onPathBrowserCurrentInput);
    $('path-browser-current').addEventListener('keydown', onPathBrowserCurrentKeydown);
    $('path-browser-pick').addEventListener('click', confirmPathBrowserPick);
    $('path-browser-list').addEventListener('click', onPathBrowserListClick);
    $('path-browser-list').addEventListener('dblclick', onPathBrowserListDblClick);
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
    $('task-save').addEventListener('click', saveTask);
    $('task-delete').addEventListener('click', deleteTask);
    $('btn-run-all').addEventListener('click', runAll);
    function updateScheduleUI() {
        const enabled = $('task-schedule-enabled').checked;
        $('task-schedule-settings').hidden = !enabled;
        const described = describeCron($('task-schedule-cron').value);
        const description = $('task-schedule-description');
        description.textContent = described.text;
        description.classList.toggle('error', !described.valid);
        $('task-schedule-cron').setAttribute('aria-invalid', String(!described.valid));
    }
    ['task-name', 'task-output'].forEach(id => $(id).addEventListener('input', updateTaskOutputPreview));
    $('task-format').addEventListener('change', updateTaskOutputPreview);
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
        else if (cls.contains('r-spd-min')) rule.speedMin = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (cls.contains('r-spd-max')) rule.speedMax = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (cls.contains('c-field')) {
            const ci = Number(e.target.dataset.ci);
            rule.conditions[ci].field = e.target.value;
            // 更新占位符提示
            const input = e.target.closest('.task-condition').querySelector('.c-values');
            input.placeholder = (e.target.value === 'country' || e.target.value === 'dataCenter') ? '多值逗号分隔；留空 = 任意' : '多值逗号分隔';
        } else if (cls.contains('c-values')) {
            const ci = Number(e.target.dataset.ci);
            rule.conditions[ci].values = (e.target.value || '').split(/[,，]/).map(s => s.trim()).filter(Boolean);
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

    return { onAuto: () => {}, onDone, isAutoRunning: () => state.running, refreshLibrary: () => {} };
}