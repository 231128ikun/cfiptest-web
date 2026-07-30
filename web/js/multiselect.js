// multiselect.js —— 可搜索多选下拉（配额分组选择用）
//
// 为什么不用原生 <select multiple>：维度换成「数据中心」时候选项有 300 多个，
// 原生控件既不能搜索、也看不到每项的结果数，还得按住 Ctrl 点选——
// 手一滑就把已选的全清了。这里做成「触发按钮 + 搜索框 + 带计数的列表 + chips」。
//
// 用法：
//   const ms = createMultiSelect($('quota-picker'), { onChange: vals => {…} });
//   ms.setItems([{ value: '日本', label: '🇯🇵 日本', count: 12 }]);
//   ms.getSelected();          // ['日本']
//   ms.setSelected(['日本']);  // 静默设置，不触发 onChange
//   ms.destroy();              // 摘掉 document 上的外部点击监听

import { escapeHTML } from './columns.js';

let seq = 0;

export function createMultiSelect(container, { placeholder = '选择…', onChange } = {}) {
    const id = `ms-${++seq}`;
    let items = [];               // [{ value, label, count }]
    const selected = new Set();
    let open = false;
    let active = -1;              // 键盘高亮项在【过滤后】列表里的下标
    let query = '';

    container.classList.add('multiselect');
    container.innerHTML = `
        <button type="button" class="ms-trigger" aria-haspopup="listbox"
                aria-expanded="false" aria-controls="${id}-list">
            <span class="ms-label"></span><span class="ms-caret">▾</span>
        </button>
        <div class="ms-panel" hidden>
            <input type="text" class="ms-search" placeholder="搜索…" aria-label="搜索选项">
            <div class="ms-list" id="${id}-list" role="listbox" aria-multiselectable="true"></div>
            <div class="ms-actions">
                <button type="button" class="small ms-all">全选（当前搜索结果）</button>
                <button type="button" class="small ms-none">清空</button>
            </div>
        </div>
        <div class="ms-chips"></div>`;

    const trigger = container.querySelector('.ms-trigger');
    const labelEl = container.querySelector('.ms-label');
    const panel = container.querySelector('.ms-panel');
    const search = container.querySelector('.ms-search');
    const list = container.querySelector('.ms-list');
    const chips = container.querySelector('.ms-chips');

    /** 当前搜索词过滤后的选项。搜索同时匹配 value 与 label（label 带国旗）。 */
    function filtered() {
        if (!query) return items;
        return items.filter(it =>
            String(it.value).toLowerCase().includes(query)
            || String(it.label ?? '').toLowerCase().includes(query));
    }

    function renderLabel() {
        const n = selected.size;
        labelEl.textContent = n ? `已选 ${n} 项` : placeholder;
        labelEl.classList.toggle('placeholder', n === 0);
    }

    function renderChips() {
        // chips 只展示已选项，是「不展开面板也能看清选了什么」的唯一途径。
        // 用 data-value 而不是下标：items 换维度后下标会全部错位。
        chips.innerHTML = [...selected].map(v => {
            const it = items.find(x => x.value === v);
            return `<span class="ms-chip">${escapeHTML(it?.label ?? v)}<button type="button"
                    class="ms-chip-x" data-value="${escapeHTML(v)}" aria-label="移除 ${escapeHTML(v)}">×</button></span>`;
        }).join('');
    }

    function renderList() {
        const fl = filtered();
        if (!fl.length) {
            list.innerHTML = `<div class="ms-empty">${items.length ? '无匹配项' : '暂无可选项'}</div>`;
            return;
        }
        list.innerHTML = fl.map((it, i) => {
            const on = selected.has(it.value);
            return `<div class="ms-opt${on ? ' selected' : ''}${i === active ? ' active' : ''}"
                         role="option" aria-selected="${on}" data-value="${escapeHTML(it.value)}">
                        <span class="ms-check">${on ? '✓' : ''}</span>
                        <span class="ms-opt-label">${escapeHTML(it.label ?? it.value)}</span>
                        <span class="ms-opt-count">${it.count ?? ''}</span>
                    </div>`;
        }).join('');
    }

    function render() {
        renderLabel();
        renderList();
        renderChips();
    }

    function emit() {
        onChange?.(getSelected());
    }

    function getSelected() {
        // 按 items 的顺序返回（= 调用方给的顺序，通常是按计数降序），
        // 而不是按点选顺序——否则同一份选择在界面上的呈现次序会随手速变化。
        return items.filter(it => selected.has(it.value)).map(it => it.value);
    }

    function toggle(value) {
        if (selected.has(value)) selected.delete(value);
        else selected.add(value);
        render();
        emit();
    }

    function setOpen(next) {
        open = next;
        panel.hidden = !open;
        trigger.setAttribute('aria-expanded', String(open));
        container.classList.toggle('open', open);
        if (open) {
            active = -1;
            search.focus();
            search.select();
        }
    }

    trigger.addEventListener('click', () => setOpen(!open));

    search.addEventListener('input', e => {
        query = e.target.value.trim().toLowerCase();
        active = -1;            // 搜索结果变了，旧的高亮下标已经没有意义
        renderList();
    });

    // 键盘操作在搜索框上监听：面板一打开焦点就在这儿，
    // 用户不必先 Tab 到列表才能用方向键。
    search.addEventListener('keydown', e => {
        const fl = filtered();
        if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault();
            if (!fl.length) return;
            const d = e.key === 'ArrowDown' ? 1 : -1;
            active = (active + d + fl.length) % fl.length;
            renderList();
            list.querySelector('.ms-opt.active')?.scrollIntoView({ block: 'nearest' });
        } else if (e.key === 'Enter') {
            e.preventDefault();
            if (active >= 0 && active < fl.length) toggle(fl[active].value);
        } else if (e.key === 'Escape') {
            e.preventDefault();
            setOpen(false);
            trigger.focus();
        }
    });

    list.addEventListener('click', e => {
        const opt = e.target.closest('.ms-opt');
        if (opt) toggle(opt.dataset.value);
    });

    // 「全选」只作用于当前搜索结果，不是全部候选项——
    // 搜「日本」再点全选，期望的是选中日本相关的那几项而不是 300 个数据中心。
    container.querySelector('.ms-all').addEventListener('click', () => {
        filtered().forEach(it => selected.add(it.value));
        render();
        emit();
    });

    container.querySelector('.ms-none').addEventListener('click', () => {
        selected.clear();
        render();
        emit();
    });

    chips.addEventListener('click', e => {
        const x = e.target.closest('.ms-chip-x');
        if (x) toggle(x.dataset.value);
    });

    // 点面板外面关闭。挂在 document 上，所以必须能摘掉（destroy），
    // 否则换维度重建控件时会一直堆积僵尸监听。
    const onDocClick = e => {
        if (open && !container.contains(e.target)) setOpen(false);
    };
    document.addEventListener('click', onDocClick);

    render();

    return {
        /** 换一批候选项（换维度时调用）。已选中但新列表里不存在的值会被丢弃。 */
        setItems(next) {
            items = Array.isArray(next) ? next : [];
            const valid = new Set(items.map(it => it.value));
            let dropped = false;
            for (const v of [...selected]) {
                if (!valid.has(v)) { selected.delete(v); dropped = true; }
            }
            active = -1;
            render();
            if (dropped) emit();   // 选择集实际变了，调用方需要知道
        },
        getSelected,
        /** 静默设置选择（不触发 onChange），用于外部状态回填。 */
        setSelected(values) {
            selected.clear();
            const valid = new Set(items.map(it => it.value));
            (values || []).forEach(v => { if (valid.has(v)) selected.add(v); });
            render();
        },
        destroy() {
            document.removeEventListener('click', onDocClick);
            container.innerHTML = '';
            container.classList.remove('multiselect', 'open');
        },
    };
}
