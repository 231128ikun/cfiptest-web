// auto.js —— 自动化维护页：订阅器表单编辑、模板复用（导出页同一套）、一键运行与进度
import { PRESETS } from './composer.js';
import { escapeHTML } from './columns.js';
import * as api from './api.js';

const $ = id => document.getElementById(id);
const SAVED_TEMPLATE_KEY = 'iptest.savedTemplates.v1';

const fmtTime = ts => {
    if (!ts) return '—';
    const d = new Date(ts);
    if (Number.isNaN(d.getTime())) return '—';
    const diff = Date.now() - d.getTime();
    if (diff < 60_000) return '刚刚';
    if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
    if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
    return d.toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
};
const fmtSpeed = kbs => (kbs > 0 ? `${Math.round(kbs)} kB/s` : '—');
const fmtLatency = ms => (ms > 0 ? `${ms} ms` : '—');

export function initAuto({ toast }) {
    const state = {
        subs: [],
        index: -1, // 当前编辑的订阅器下标（-1 = 未选择/新建草稿）
        savedTemplates: [],
        running: false,
        report: null,
    };

    function currentSub() {
        return state.index >= 0 && state.index < state.subs.length ? state.subs[state.index] : null;
    }

    function log(line, kind = '') {
        const box = $('auto-log');
        const div = document.createElement('div');
        div.className = `auto-log-line ${kind}`;
        div.textContent = `[${new Date().toLocaleTimeString('zh-CN', { hour12: false })}] ${line}`;
        box.appendChild(div);
        box.scrollTop = box.scrollHeight;
    }

    function setRunning(running) {
        state.running = running;
        $('btn-auto-run').disabled = running;
        $('btn-auto-stop').disabled = !running;
        $('auto-run-status').textContent = running ? '运行中…' : '';
    }

    function showReport(report) {
        state.report = report;
        const box = $('auto-report');
        box.hidden = !report;
        if (!report) return;
        const rows = (report.groups || []).map(g => `
            <tr>
                <td>${escapeHTML(g.name || '')}</td>
                <td>${g.filled ?? 0} / ${g.target ?? 0}</td>
                <td class="${g.shortage > 0 ? 'bad-shortage' : ''}">${g.shortage ?? 0}</td>
                <td>${g.tested ?? 0}</td>
                <td>${g.failed ?? 0}</td>
                <td>${g.speedTested ?? 0}</td>
                <td>${g.speedFailed ?? 0}</td>
                <td>${g.updated ?? 0}</td>
            </tr>`).join('');
        const shortages = (report.shortages || []).map(s => `<div class="auto-shortage">⚠ ${escapeHTML(s)}</div>`).join('');
        const inputLine = (report.inputAdded || report.inputUpdated)
            ? `<div class="auto-report-row">输入订阅文件：新增 ${report.inputAdded ?? 0} 条，更新 ${report.inputUpdated ?? 0} 条</div>` : '';
        const link = report.outputPath
            ? `<div class="auto-report-row">输出文件：<a href="${api.autoOutputUrl(report.outputPath.replace(/\\/g, '/').replace(/^.*[\\/]data[\\/]/, 'out/'))}" download>下载 ${escapeHTML(report.outputPath)}</a></div>`
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

    // ---- 模板（复用导出页：内置模板 + 我的模板 + 自定义） ----
    function loadTemplates() {
        try {
            const parsed = JSON.parse(localStorage.getItem(SAVED_TEMPLATE_KEY) || '[]');
            state.savedTemplates = Array.isArray(parsed)
                ? parsed.filter(item => item && typeof item.name === 'string' && typeof item.template === 'string')
                : [];
        } catch {
            state.savedTemplates = [];
        }
        renderTemplateOptions();
    }

    async function fetchSettingsTemplates() {
        try {
            const config = await api.fetchConfig();
            const list = config?.settings?.savedTemplates;
            if (Array.isArray(list)) {
                state.savedTemplates = list.filter(item => item && typeof item.name === 'string' && typeof item.template === 'string');
                renderTemplateOptions();
            }
        } catch { /* 后端不可用时沿用本地缓存 */ }
    }

    function templateValueToOption(template) {
        const pIdx = PRESETS.findIndex(p => p.template === template);
        if (pIdx >= 0) return `preset:${pIdx}`;
        const sIdx = state.savedTemplates.findIndex(p => p.template === template);
        if (sIdx >= 0) return `saved:${sIdx}`;
        return 'custom';
    }

    function renderTemplateOptions(selected = '') {
        const presetOptions = PRESETS.map((p, i) => `<option value="preset:${i}">${escapeHTML(p.name)}</option>`).join('');
        const savedOptions = state.savedTemplates.map((p, i) => `<option value="saved:${i}">${escapeHTML(p.name)}</option>`).join('');
        const sel = $('auto-tpl-select');
        sel.innerHTML = `<optgroup label="内置模板">${presetOptions}</optgroup>`
            + (savedOptions ? `<optgroup label="我的模板">${savedOptions}</optgroup>` : '')
            + '<optgroup label="自定义"><option value="custom">自定义…</option></optgroup>';
        sel.value = selected;
    }

    function templateFor(optionValue) {
        if (typeof optionValue === 'string' && optionValue.startsWith('preset:')) {
            return PRESETS[Number(optionValue.slice(7))]?.template ?? '';
        }
        if (typeof optionValue === 'string' && optionValue.startsWith('saved:')) {
            return state.savedTemplates[Number(optionValue.slice(6))]?.template ?? '';
        }
        return '';
    }

    async function saveCurrentTemplate() {
        const tpl = $('auto-tpl-custom').value.trim();
        if (!tpl) { toast('模板为空，先输入或选择一个模板'); return; }
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
        renderTemplateOptions(`saved:${state.savedTemplates.length - 1}`);
        $('auto-tpl-custom').value = tpl;
        toast('模板已保存');
    }

    // ---- 订阅器表单 ----
    function renderSubSelect() {
        const sel = $('auto-sub-select');
        sel.innerHTML = state.subs.map((s, i) => `<option value="${i}">${escapeHTML(s.name || `订阅器 ${i + 1}`)}</option>`).join('')
            || '<option value="-1">（暂无订阅器，点「新建」）</option>';
        sel.value = String(state.index);
        $('btn-auto-sub-delete').disabled = state.index < 0;
    }

    function renderGroupRows() {
        const sub = currentSub();
        const groups = sub?.groups ?? [];
        const wrap = $('auto-groups-rows');
        if (groups.length === 0) {
            wrap.innerHTML = '<div class="auto-groups-empty">暂无分组，点击「添加分组」</div>';
            return;
        }
        wrap.innerHTML = groups.map((g, i) => `
            <div class="auto-group-row" data-i="${i}">
                <input class="g-name" data-i="${i}" value="${escapeHTML(g.name || '')}" placeholder="名称">
                <input class="g-country" data-i="${i}" value="${escapeHTML((g.countryCode || '').toUpperCase())}" placeholder="US/JP/SG">
                <input class="g-ports" data-i="${i}" value="${escapeHTML((g.ports || []).join(','))}" placeholder="443,2053">
                <input class="g-latency" data-i="${i}" type="number" min="0" value="${g.maxLatencyMs || ''}" placeholder="300">
                <input class="g-speed" data-i="${i}" type="number" min="0" value="${g.minSpeedKBs || ''}" placeholder="1000">
                <label class="auto-group-require" title="需有效测速结果才入订阅"><input class="g-require" data-i="${i}" type="checkbox" ${g.requireSpeed ? 'checked' : ''}>测速</label>
                <input class="g-count" data-i="${i}" type="number" min="1" value="${g.count ?? 1}">
                <button type="button" class="g-del" data-i="${i}" title="删除分组">✕</button>
            </div>`).join('');
    }

    function fillForm() {
        const sub = currentSub();
        $('auto-sub-name').value = sub?.name ?? '';
        $('auto-sub-input').value = sub?.inputPath ?? '';
        $('auto-sub-output').value = sub?.output?.path ?? '';
        $('auto-sub-format').value = (sub?.output?.format === 'csv') ? 'csv' : 'txt';
        $('auto-sub-speed').checked = Boolean(sub?.enableSpeed);
        const template = sub?.output?.template || '{ip}:{port}#{emoji}{country}';
        $('auto-tpl-custom').value = template;
        renderTemplateOptions(templateValueToOption(template));
        renderGroupRows();
        $('auto-subs-status').textContent = '';
    }

    function collectForm() {
        const num = (el) => {
            const v = Number(el.value);
            return Number.isFinite(v) && v > 0 ? v : 0;
        };
        const groups = [...document.querySelectorAll('#auto-groups-rows .auto-group-row')].map(row => {
            const q = sel => row.querySelector(sel);
            const ports = (q('.g-ports')?.value || '').split(/[,，]/).map(s => Number(s.trim())).filter(n => n > 0);
            return {
                name: q('.g-name').value.trim() || undefined,
                countryCode: q('.g-country').value.trim().toUpperCase() || undefined,
                ports: ports.length ? ports : undefined,
                maxLatencyMs: num(q('.g-latency')) || undefined,
                minSpeedKBs: num(q('.g-speed')) || undefined,
                requireSpeed: q('.g-require').checked,
                count: Math.max(1, Math.trunc(Number(q('.g-count').value) || 1)),
            };
        });
        return {
            name: $('auto-sub-name').value.trim(),
            inputPath: $('auto-sub-input').value.trim() || undefined,
            enableSpeed: $('auto-sub-speed').checked,
            groups,
            output: {
                path: $('auto-sub-output').value.trim() || undefined,
                format: $('auto-sub-format').value,
                template: $('auto-tpl-custom').value.trim(),
            },
        };
    }

    async function loadSubs() {
        try {
            const data = await api.fetchAutoSubs();
            state.subs = data.subscriptions || [];
            state.index = state.subs.length ? 0 : -1;
            renderSubSelect();
            fillForm();
            refreshRunSelect();
        } catch (error) {
            toast(`加载订阅器失败：${error.message}`);
        }
    }

    function refreshRunSelect() {
        const sel = $('auto-run-select');
        sel.innerHTML = state.subs.map(s => `<option value="${escapeHTML(s.name)}">${escapeHTML(s.name)}</option>`).join('')
            || '<option value="">（暂无订阅器，先在上方新建并保存）</option>';
        sel.disabled = state.subs.length === 0;
    }

    async function saveSubs() {
        const sub = collectForm();
        if (!sub.name) { toast('订阅器名称不能为空'); return; }
        if (!sub.groups.length) { toast('至少需要一个分组'); return; }
        try {
            await api.validateAutoSub(sub);
        } catch (error) {
            toast(`校验失败：${error.message}`);
            return;
        }
        if (state.index >= 0 && state.index < state.subs.length) {
            state.subs[state.index] = sub;
        } else {
            state.subs.push(sub);
            state.index = state.subs.length - 1;
        }
        try {
            await api.saveAutoSubs(state.subs);
            toast('订阅器已保存');
            $('auto-subs-status').textContent = '已保存到 data/subscriptions.json';
            renderSubSelect();
            refreshRunSelect();
        } catch (error) {
            toast(`保存失败：${error.message}`);
        }
    }

    function deleteCurrentSub() {
        if (state.index < 0) return;
        const name = state.subs[state.index]?.name || '';
        if (!confirm(`确认删除订阅器「${name}」？`)) return;
        state.subs.splice(state.index, 1);
        state.index = state.subs.length ? 0 : -1;
        renderSubSelect();
        fillForm();
        refreshRunSelect();
        api.saveAutoSubs(state.subs).then(
            () => { toast('已删除'); $('auto-subs-status').textContent = '已保存到 data/subscriptions.json'; },
            error => toast(`保存失败：${error.message}`),
        );
    }

    // ---- 库统计（导入入口在检测结果页） ----
    async function refreshStats() {
        try {
            const data = await api.fetchAutoLibrary({ limit: 1 });
            const st = data.stats || {};
            $('auto-lib-total').textContent = `库 ${st.total ?? 0} 条（有效 ${st.active ?? 0} / 未测 ${st.new ?? 0}）`;
        } catch { /* 忽略 */ }
    }

    // ---- 运行 ----
    async function run() {
        const name = $('auto-run-select').value;
        if (!name) {
            toast('请先在上方新建并保存订阅器');
            return;
        }
        try {
            const result = await api.runAuto(name);
            state.report = null;
            $('auto-report').hidden = true;
            $('auto-output-link').hidden = true;
            setRunning(true);
            log(`启动维护：${name}（taskId=${result.taskId}）`);
        } catch (error) {
            toast(`启动失败：${error.message}`);
            log(`启动失败：${error.message}`, 'error');
        }
    }

    function stop() {
        api.stopTask('').catch(() => {});
        log('正在停止…', 'warn');
    }

    function onAuto(message) {
        if (!message) return;
        let p;
        try {
            p = JSON.parse(message);
        } catch {
            return;
        }
        if (p.stage === 'report' && p.report) {
            showReport(p.report);
            return;
        }
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
        setRunning(false);
        if (reason === 'stopped') {
            log('已停止', 'warn');
        } else if (message && message.startsWith('自动化完成')) {
            log(message, 'ok');
            $('auto-run-status').textContent = '完成';
            $('auto-output-link').hidden = false;
        } else {
            log(message || '运行结束', 'error');
        }
        refreshStats();
        loadSubs();
    }

    // ---- 事件绑定 ----
    $('auto-sub-select').addEventListener('change', e => {
        state.index = Number(e.target.value);
        renderSubSelect();
        fillForm();
    });
    $('btn-auto-sub-new').addEventListener('click', () => {
        state.subs.push({ name: '新订阅器', enableSpeed: false, groups: [{ name: '分组1', countryCode: '', count: 10 }], output: { format: 'txt', template: '{ip}:{port}#{emoji}{country}' } });
        state.index = state.subs.length - 1;
        renderSubSelect();
        fillForm();
    });
    $('btn-auto-sub-delete').addEventListener('click', deleteCurrentSub);
    $('btn-auto-save').addEventListener('click', saveSubs);
    $('auto-tpl-select').addEventListener('change', e => {
        const tpl = templateFor(e.target.value);
        if (tpl) $('auto-tpl-custom').value = tpl;
    });
    $('btn-auto-tpl-save').addEventListener('click', saveCurrentTemplate);
    $('btn-auto-group-add').addEventListener('click', () => {
        const sub = currentSub() || { groups: [] };
        sub.groups = sub.groups || [];
        sub.groups.push({ name: `分组${sub.groups.length + 1}`, count: 10 });
        if (!currentSub()) { state.subs.push(sub); state.index = state.subs.length - 1; renderSubSelect(); }
        renderGroupRows();
    });
    $('auto-groups-rows').addEventListener('input', e => {
        const row = e.target.closest('.auto-group-row');
        if (!row) return;
        const sub = currentSub();
        if (!sub) return;
        const g = sub.groups[Number(row.dataset.i)];
        if (!g) return;
        const cls = [...e.target.classList].find(c => c.startsWith('g-'));
        if (!cls) return;
        if (cls === 'g-require') g.requireSpeed = e.target.checked;
        else if (cls === 'g-count') g.count = Math.max(1, Math.trunc(Number(e.target.value) || 1));
        else if (cls === 'g-name') g.name = e.target.value;
        else if (cls === 'g-country') g.countryCode = e.target.value.trim().toUpperCase();
        else if (cls === 'g-ports') g.ports = e.target.value.split(/[,，]/).map(s => Number(s.trim())).filter(n => n > 0);
        else if (cls === 'g-latency') g.maxLatencyMs = Number(e.target.value) > 0 ? Number(e.target.value) : undefined;
        else if (cls === 'g-speed') g.minSpeedKBs = Number(e.target.value) > 0 ? Number(e.target.value) : undefined;
    });
    $('auto-groups-rows').addEventListener('click', e => {
        if (!e.target.classList.contains('g-del')) return;
        const sub = currentSub();
        if (!sub) return;
        sub.groups.splice(Number(e.target.dataset.i), 1);
        renderGroupRows();
    });
    $('btn-auto-run').addEventListener('click', run);
    $('btn-auto-stop').addEventListener('click', stop);

    // 页面加载时同步运行状态
    api.fetchTaskStatus().then(status => {
        if (status.status === 'running' && (status.taskId || '').startsWith('auto:')) {
            setRunning(true);
            log('检测到自动化任务正在后台运行…', 'info');
        }
    }).catch(() => {});

    loadTemplates();
    fetchSettingsTemplates();
    loadSubs();
    refreshStats();

    return { onAuto, onDone, isAutoRunning: () => state.running, refreshLibrary: refreshStats };
}
