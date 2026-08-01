// tasks.js —— 自动维护页：任务列表（Master-Detail）+ 规则编辑器 + 一键维护
import { PRESETS } from './composer.js';
import { escapeHTML } from './columns.js';
import * as api from './api.js';

const $ = id => document.getElementById(id);
const SAVED_TEMPLATE_KEY = 'iptest.savedTemplates.v1';
const FIELD_LABEL = { country: '国家', city: '城市', port: '端口' };

let seq = 0;

export function initTasks({ toast }) {
    const state = {
        tasks: [],
        libraries: [],
        libNames: {},
        selected: -1,
        savedTemplates: [],
        running: false,
        runningTaskId: null,
        runQueue: [],
        lastRuns: new Map(), // taskId -> {status, totalLines, outputPath}
    };

    function currentTask() {
        return state.selected >= 0 && state.selected < state.tasks.length ? state.tasks[state.selected] : null;
    }

    // ---- 日志 ----
    function log(line, kind = '') {
        const box = $('task-log');
        const div = document.createElement('div');
        div.className = `auto-log-line ${kind}`;
        div.textContent = `[${new Date().toLocaleTimeString('zh-CN', { hour12: false })}] ${line}`;
        box.appendChild(div);
        box.scrollTop = box.scrollHeight;
    }

    function setRunning(running) {
        state.running = running;
        $('btn-run-all').disabled = running;
        $('btn-task-stop').disabled = !running;
        const runBtn = $('task-run');
        if (runBtn) runBtn.disabled = running;
        $('task-log-status').textContent = running ? '运行中…' : '';
    }

    function showReport(report) {
        const box = $('task-report');
        box.hidden = !report;
        if (!report) return;
        const rows = (report.groups || []).map(g => `
            <tr>
                <td>${escapeHTML(g.name || '')}</td>
                <td>${g.filled ?? 0}${g.target ? ` / ${g.target}` : ' / 不限'}</td>
                <td class="${g.shortage > 0 ? 'bad-shortage' : ''}">${g.shortage ?? 0}</td>
                <td>${g.tested ?? 0}</td>
                <td>${g.failed ?? 0}</td>
                <td>${g.speedTested ?? 0}</td>
                <td>${g.speedFailed ?? 0}</td>
                <td>${g.updated ?? 0}</td>
            </tr>`).join('');
        const shortages = (report.shortages || []).map(s => `<div class="auto-shortage">⚠ ${escapeHTML(s)}</div>`).join('');
        const inputLine = (report.inputAdded || report.inputUpdated)
            ? `<div class="auto-report-row">输入文件：新增 ${report.inputAdded ?? 0} 条，更新 ${report.inputUpdated ?? 0} 条</div>` : '';
        const link = report.outputPath
            ? `<div class="auto-report-row">输出：<a href="${api.autoOutputUrl(report.outputPath.replace(/\\/g, '/').replace(/^.*[\\/]data[\\/]/, 'out/'))}" download>下载 ${escapeHTML(report.outputPath)}</a></div>`
            : '';
        box.innerHTML = `
            <div class="auto-report-head">本次运行（${Math.round((report.durationMs ?? 0) / 1000)}s）</div>
            <table class="results auto-report-table">
                <thead><tr><th>分组</th><th>配额</th><th>缺口</th><th>延迟测试</th><th>延迟失败(移除)</th><th>测速</th><th>测速失败(保留)</th><th>回写更新</th></tr></thead>
                <tbody>${rows}</tbody>
            </table>
            ${inputLine}
            ${shortages}
            ${link}
            <div class="auto-report-row">共输出 ${report.totalLines ?? 0} 行，移除失效 ${report.removedDead ?? 0} 条</div>`;
    }

    // ---- 数据加载 ----
    async function loadAll() {
        try {
            const [tasksData, libsData] = await Promise.all([api.fetchTasks(), api.fetchLibraries()]);
            state.tasks = tasksData.tasks || [];
            state.libraries = libsData.libraries || [];
            state.libNames = {};
            (state.libraries || []).forEach(l => { state.libNames[l.id] = l.name; });
            if (state.selected >= state.tasks.length) state.selected = state.tasks.length ? state.tasks.length - 1 : -1;
            renderTaskList();
            renderLibraryOptions();
            fillForm();
        } catch (error) {
            toast(`加载失败：${error.message}`);
        }
    }

    async function loadLastRuns() {
        try {
            const data = await api.fetchRuns(80);
            state.lastRuns.clear();
            (data.runs || []).forEach(r => {
                if (!state.lastRuns.has(r.taskId) || state.lastRuns.get(r.taskId).startedAt < r.startedAt) {
                    state.lastRuns.set(r.taskId, r);
                }
            });
            renderTaskList();
        } catch { /* 忽略 */ }
    }

    // ---- 任务列表 ----
    function renderTaskList() {
        const wrap = $('task-list');
        if (state.tasks.length === 0) {
            wrap.innerHTML = '<div class="task-detail-empty">暂无任务，点击右上角「+ 新建任务」开始</div>';
            return;
        }
        wrap.innerHTML = state.tasks.map((task, i) => {
            const run = state.lastRuns.get(task.id);
            let badge = '';
            if (run) {
                const kind = run.status === 'completed' ? (run.shortages?.length ? 'shortage' : 'ok') : (run.status === 'error' ? 'error' : 'shortage');
                const label = run.status === 'completed' ? `✓ ${run.totalLines} 条` : (run.status === 'error' ? '× 出错' : '已停止');
                badge = `<span class="task-badge ${kind}" title="${escapeHTML(run.outputPath || '')}">${label}</span>`;
            } else {
                badge = '<span class="task-badge none">未运行</span>';
            }
            const ruleSummary = (task.rules || []).map(r => {
                const conds = (r.conditions || []).map(c => {
                    const vals = (c.values || []).join(',');
                    return `${FIELD_LABEL[c.field] || c.field}${vals ? ':' + vals : '不限'}`;
                }).join(' ∩ ');
                return conds || '全部';
            }).join('；');
            return `<div class="task-card ${i === state.selected ? 'selected' : ''}" data-i="${i}">
                <div class="task-card-head">
                    <span class="task-card-name">${escapeHTML(task.name || '未命名')}</span>
                    ${badge}
                </div>
                <div class="task-card-summary">
                    ${task.enabled ? '' : '（已停用）'}规则：${escapeHTML(ruleSummary)}<br>
                    库：${escapeHTML(state.libNames[task.libraryId] || task.libraryId || '默认库')} · 输出：${escapeHTML(task.output?.path || '未设置')}
                </div>
                <div class="task-card-actions" style="margin-top:8px">
                    <label class="checkbox" title="一键维护时是否执行"><input type="checkbox" class="task-enable" data-i="${i}" ${task.enabled ? 'checked' : ''}> 启用</label>
                    <button type="button" class="small primary task-run-one" data-i="${i}">维护</button>
                </div>
            </div>`;
        }).join('');
    }

    // ---- 库下拉 ----
    function renderLibraryOptions() {
        const sel = $('task-library');
        sel.innerHTML = (state.libraries || []).map(l => `<option value="${escapeHTML(l.id)}">${escapeHTML(l.name)}</option>`).join('');
    }

    // ---- 模板（复用导出页） ----
    function loadTemplates() {
        try {
            const parsed = JSON.parse(localStorage.getItem(SAVED_TEMPLATE_KEY) || '[]');
            state.savedTemplates = Array.isArray(parsed)
                ? parsed.filter(item => item && typeof item.name === 'string' && typeof item.template === 'string')
                : [];
        } catch {
            state.savedTemplates = [];
        }
    }

    async function fetchSettingsTemplates() {
        try {
            const config = await api.fetchConfig();
            const list = config?.settings?.savedTemplates;
            if (Array.isArray(list)) state.savedTemplates = list.filter(item => item && typeof item.name === 'string' && typeof item.template === 'string');
        } catch { /* 忽略 */ }
    }

    function tplOptionFor(template) {
        const p = PRESETS.findIndex(x => x.template === template);
        if (p >= 0) return `preset:${p}`;
        const s = state.savedTemplates.findIndex(x => x.template === template);
        if (s >= 0) return `saved:${s}`;
        return 'custom';
    }

    function renderTplSelect(selected = '') {
        const presetOpts = PRESETS.map((p, i) => `<option value="preset:${i}">${escapeHTML(p.name)}</option>`).join('');
        const savedOpts = state.savedTemplates.map((p, i) => `<option value="saved:${i}">${escapeHTML(p.name)}</option>`).join('');
        const sel = $('task-tpl-select');
        sel.innerHTML = `<optgroup label="内置模板">${presetOpts}</optgroup>`
            + (savedOpts ? `<optgroup label="我的模板">${savedOpts}</optgroup>` : '')
            + '<optgroup label="自定义"><option value="custom">自定义…</option></optgroup>';
        sel.value = selected;
    }

    function tplFor(optionValue) {
        if (typeof optionValue === 'string' && optionValue.startsWith('preset:')) return PRESETS[Number(optionValue.slice(7))]?.template ?? '';
        if (typeof optionValue === 'string' && optionValue.startsWith('saved:')) return state.savedTemplates[Number(optionValue.slice(6))]?.template ?? '';
        return '';
    }

    async function saveCurrentTemplate() {
        const tpl = $('task-tpl-custom').value.trim();
        if (!tpl) { toast('模板为空'); return; }
        const name = prompt('模板名称（与导出页「我的模板」共用）', `我的模板 ${state.savedTemplates.length + 1}`);
        if (!name) return;
        state.savedTemplates = [...state.savedTemplates.filter(t => t.template !== tpl), { name: name.trim(), template: tpl }];
        try {
            localStorage.setItem(SAVED_TEMPLATE_KEY, JSON.stringify(state.savedTemplates));
            const config = await api.fetchConfig();
            await api.saveSettings({ ...(config.settings || {}), savedTemplates: state.savedTemplates });
        } catch (error) {
            toast(`保存模板失败：${error.message}`);
            return;
        }
        renderTplSelect(`saved:${state.savedTemplates.length - 1}`);
        $('task-tpl-custom').value = tpl;
        toast('模板已保存');
    }

    // ---- 表单 ----
    function fillForm() {
        const task = currentTask();
        const form = $('task-form');
        const empty = $('task-detail-empty');
        form.hidden = !task;
        empty.hidden = !!task;
        if (!task) return;
        $('task-name').value = task.name || '';
        $('task-enabled').checked = task.enabled !== false;
        if (state.libraries.some(l => l.id === task.libraryId)) $('task-library').value = task.libraryId;
        $('task-input').value = task.input?.file || '';
        $('task-output').value = task.output?.path || '';
        $('task-format').value = task.output?.format === 'csv' ? 'csv' : 'txt';
        $('task-limit').value = task.limit || 0;
        $('task-speed').checked = Boolean(task.speedEnabled);
        const tpl = task.output?.template || '{ip}:{port}#{emoji}{country}';
        $('task-tpl-custom').value = tpl;
        renderTplSelect(tplOptionFor(tpl));
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
                                <option value="country" ${c.field === 'country' ? 'selected' : ''}>国家</option>
                                <option value="city" ${c.field === 'city' ? 'selected' : ''}>城市</option>
                                <option value="port" ${c.field === 'port' ? 'selected' : ''}>端口</option>
                            </select>
                            <input class="c-values" data-ri="${ri}" data-ci="${ci}" value="${escapeHTML((c.values || []).join(','))}" placeholder="多值用逗号分隔，如 US,JP；留空 = 不限">
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
        task.enabled = $('task-enabled').checked;
        task.libraryId = $('task-library').value || 'default';
        task.input = { mode: 'file', file: $('task-input').value.trim() || undefined };
        task.output = {
            path: $('task-output').value.trim() || undefined,
            format: $('task-format').value,
            template: $('task-tpl-custom').value.trim(),
        };
        task.limit = Number($('task-limit').value) > 0 ? Number($('task-limit').value) : 0;
        task.speedEnabled = $('task-speed').checked;
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
            // 国家值大写；端口值转数字校验由后端完成
            rule.conditions.forEach(c => { if (c.field === 'country') c.values = c.values.map(v => v.toUpperCase()); });
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
        if (state.selected >= 0 && state.selected < state.tasks.length) {
            state.tasks[state.selected] = task;
        } else {
            state.tasks.push(task);
            state.selected = state.tasks.length - 1;
        }
        try {
            await api.saveTasks(state.tasks);
            toast('任务已保存');
            $('task-form-status').textContent = '已保存到 data/tasks.json';
            renderTaskList();
        } catch (error) {
            toast(`保存失败：${error.message}`);
        }
    }

    function newTask() {
        state.tasks.push({
            id: `t-${Date.now().toString(36)}`,
            name: '新任务', enabled: true, libraryId: 'default',
            input: { mode: 'file' },
            output: { format: 'txt', template: '{ip}:{port}#{emoji}{country}' },
            limit: 0, speedEnabled: false,
            rules: [{ name: '规则 1', conditions: [], limit: 0 }],
        });
        state.selected = state.tasks.length - 1;
        renderTaskList();
        fillForm();
    }

    async function deleteTask() {
        const task = currentTask();
        if (!task) return;
        if (!confirm(`确认删除任务「${task.name}」？`)) return;
        state.tasks.splice(state.selected, 1);
        state.selected = state.tasks.length ? Math.min(state.selected, state.tasks.length - 1) : -1;
        renderTaskList();
        fillForm();
        try {
            await api.saveTasks(state.tasks);
            toast('任务已删除');
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
            state.report = null;
            $('task-report').hidden = true;
            log(`启动维护：${taskId}（taskId=${result.taskId}）`);
        } catch (error) {
            toast(`启动失败：${error.message}`);
            log(`启动失败：${error.message}`, 'error');
        }
    }

    function runSelected() {
        const task = currentTask();
        if (!task) { toast('请先选择任务'); return; }
        if (state.running) { toast('已有任务在运行'); return; }
        runTaskId(task.id);
    }

    function runAll() {
        if (state.running) { toast('已有任务在运行'); return; }
        const enabled = state.tasks.filter(t => t.enabled && t.id);
        if (!enabled.length) { toast('没有已启用的任务（先勾选任务卡片上的「启用」）'); return; }
        state.runQueue = enabled.map(t => t.id);
        log(`一键维护：共 ${state.runQueue.length} 个任务`);
        runNextInQueue();
    }

    function runNextInQueue() {
        if (!state.runQueue.length) {
            setRunning(false);
            log('一键维护全部完成', 'ok');
            return;
        }
        runTaskId(state.runQueue.shift());
    }

    function stop() {
        api.stopTask('').catch(() => {});
        log('正在停止…', 'warn');
    }

    function onAuto(message) {
        if (!message) return;
        let p;
        try { p = JSON.parse(message); } catch { return; }
        if (p.stage === 'report' && p.report) { showReport(p.report); return; }
        const prefix = p.group ? `[${p.group}] ` : '';
        switch (p.stage) {
            case 'input':
            case 'gather':
                log(`${prefix}${p.log || `收集候选 ${p.tested} 条`}`, 'info');
                break;
            case 'latency':
                log(`${prefix}延迟检测完成：通过 ${p.passed}，失败 ${p.failed}（已从库移除）`, p.failed > 0 ? 'warn' : 'ok');
                break;
            case 'speed':
                log(`${prefix}测速完成：有效 ${p.tested - p.failed}，失败 ${p.failed}（保留待下次验证）`, p.failed > 0 ? 'warn' : 'ok');
                break;
            case 'output':
                log(`${prefix}${p.log || '已写出订阅文件'}`, 'ok');
                break;
            default:
                if (p.log) log(`${prefix}${p.log}`);
        }
    }

    function onDone(message, reason) {
        if (!state.running) return;
        if (reason === 'stopped') {
            setRunning(false);
            state.runQueue = [];
            log('已停止', 'warn');
        } else if (message && message.startsWith('自动化完成')) {
            log(message, 'ok');
            $('task-log-status').textContent = '完成';
        } else {
            log(message || '运行结束', 'error');
        }
        loadLastRuns();
        loadAll();
        if (state.runQueue.length) {
            setTimeout(runNextInQueue, 600);
        } else {
            setRunning(false);
        }
    }

    // ---- 事件绑定 ----
    $('task-list').addEventListener('click', e => {
        const card = e.target.closest('.task-card');
        if (card && !e.target.closest('input,button')) {
            state.selected = Number(card.dataset.i);
            renderTaskList();
            fillForm();
            return;
        }
        const enable = e.target.closest('.task-enable');
        if (enable) {
            const i = Number(enable.dataset.i);
            state.tasks[i].enabled = enable.checked;
            api.saveTasks(state.tasks).catch(() => {});
            return;
        }
        const runOne = e.target.closest('.task-run-one');
        if (runOne) {
            const i = Number(runOne.dataset.i);
            const task = state.tasks[i];
            if (!state.running && task.id) runTaskId(task.id);
            return;
        }
    });

    $('btn-task-new').addEventListener('click', newTask);
    $('task-save').addEventListener('click', saveTask);
    $('task-run').addEventListener('click', runSelected);
    $('task-delete').addEventListener('click', deleteTask);
    $('btn-run-all').addEventListener('click', runAll);
    $('btn-task-stop').addEventListener('click', stop);
    $('task-speed').addEventListener('change', () => { const t = currentTask(); if (t) renderRules(t.rules || []); });

    $('task-tpl-select').addEventListener('change', e => {
        const tpl = tplFor(e.target.value);
        if (tpl) $('task-tpl-custom').value = tpl;
    });
    $('task-tpl-save').addEventListener('click', saveCurrentTemplate);
    $('task-rule-add').addEventListener('click', () => {
        const task = currentTask();
        if (!task) { newTask(); }
        const t = currentTask();
        t.rules = t.rules || [];
        t.rules.push({ name: `规则 ${t.rules.length + 1}`, conditions: [], limit: 0 });
        renderRules(t.rules);
    });
    $('task-rules').addEventListener('click', e => {
        const t = currentTask();
        if (!t) return;
        const removeRule = e.target.closest('.task-rule-remove');
        if (removeRule) {
            t.rules.splice(Number(removeRule.dataset.ri), 1);
            renderRules(t.rules);
            return;
        }
        const removeCond = e.target.closest('.c-remove');
        if (removeCond) {
            const ri = Number(removeCond.dataset.ri);
            t.rules[ri].conditions.splice(Number(removeCond.dataset.ci), 1);
            renderRules(t.rules);
            return;
        }
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
        if (e.target.classList.contains('r-limit')) rule.limit = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (e.target.classList.contains('r-lat-min')) rule.latencyMin = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (e.target.classList.contains('r-lat-max')) rule.latencyMax = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (e.target.classList.contains('r-spd-min')) rule.speedMin = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (e.target.classList.contains('r-spd-max')) rule.speedMax = Number(e.target.value) > 0 ? Number(e.target.value) : 0;
        else if (e.target.classList.contains('c-field')) {
            const ci = Number(e.target.dataset.ci);
            rule.conditions[ci].field = e.target.value;
        } else if (e.target.classList.contains('c-values')) {
            const ci = Number(e.target.dataset.ci);
            rule.conditions[ci].values = (e.target.value || '').split(/[,，]/).map(s => s.trim()).filter(Boolean);
        }
    });
    $('btn-run-all').disabled = false;
    // 停止按钮（复用现有 btn-stop？任务页提供独立停止）——任务页无独立停止按钮时跳转工作台
    // 直接复用全局停止：任务运行中点击导航到工作台可停止。为简单起见，日志区上方不设停止。

    loadTemplates();
    fetchSettingsTemplates();
    loadAll();
    loadLastRuns();

    return { onAuto, onDone, isAutoRunning: () => state.running, refreshLibrary: () => {} };
}
