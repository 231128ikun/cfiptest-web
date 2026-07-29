// 临时校验脚本：确认 store.js 的解析与派生视图行为。
// 用 node 直接跑 ESM，不引入测试框架。
import assert from 'node:assert/strict';
import {
    store, resetStore, lineToTarget, targetToLine, parseLines,
    setWorkspaceFromText, addToWorkspace, addToWorkspaceFromText,
    setWorkspaceFilter, clearWorkspaceFilter, visibleWorkspace, workspaceStats,
    appendVisibleToCandidates, addToCandidates, clearCandidates, candidateTargets,
    addResult, updateSpeed, clearResults, filterIsValid, subscribe, setMode,
} from '../web/js/store.js';

let pass = 0;
const check = (name, fn) => {
    try { fn(); pass++; console.log(`  ok  ${name}`); }
    catch (e) { console.log(`  FAIL ${name}\n       ${e.message}`); process.exitCode = 1; }
};

console.log('lineToTarget:');
check('正常 IPv4', () => assert.deepEqual(lineToTarget('1.2.3.4:443'), { ip: '1.2.3.4', port: 443 }));
check('裸 IPv4 默认 443', () => assert.deepEqual(lineToTarget('1.2.3.4'), { ip: '1.2.3.4', port: 443 }));
check('中文冒号', () => assert.deepEqual(lineToTarget('1.2.3.4：2053'), { ip: '1.2.3.4', port: 2053 }));
check('空格分隔', () => assert.deepEqual(lineToTarget('1.2.3.4 8443'), { ip: '1.2.3.4', port: 8443 }));
check('IPv6 带括号', () => assert.deepEqual(lineToTarget('[2606:4700::1]:2053'), { ip: '2606:4700::1', port: 2053 }));
check('CSV 首列', () => assert.deepEqual(lineToTarget('1.2.3.4:443,日本,东京'), { ip: '1.2.3.4', port: 443 }));

// 这几条是旧 parseCleanedToTargets 会产出 NaN 端口的输入
check('残缺 IP 被拒（旧实现产出 NaN）', () => assert.equal(lineToTarget('1.2.3'), null));
check('空行被拒', () => assert.equal(lineToTarget('   '), null));
check('纯文本被拒', () => assert.equal(lineToTarget('hello world'), null));
check('端口 0 被拒', () => assert.equal(lineToTarget('1.2.3.4:0'), null));
check('端口越界被拒', () => assert.equal(lineToTarget('1.2.3.4:70000'), null));
check('IP 段越界被拒', () => assert.equal(lineToTarget('999.1.1.1:443'), null));
check('注释行被拒', () => assert.equal(lineToTarget('# 注释'), null));
check('任何输出端口都不是 NaN', () => {
    for (const s of ['1.2.3', 'x', '1.2.3.4:abc', ':::', '1.2.3.4:', '..:.']) {
        const t = lineToTarget(s);
        assert.ok(t === null || Number.isInteger(t.port), `${s} → ${JSON.stringify(t)}`);
    }
});

console.log('targetToLine:');
check('IPv4 往返', () => assert.equal(targetToLine({ ip: '1.2.3.4', port: 443 }), '1.2.3.4:443'));
check('IPv6 加括号', () => assert.equal(targetToLine({ ip: '2606:4700::1', port: 443 }), '[2606:4700::1]:443'));

console.log('parseLines:');
check('去重与计数', () => {
    const r = parseLines('1.2.3.4:443\n1.2.3.4:443\n5.6.7.8:80\n垃圾\n\n# 注释');
    assert.equal(r.targets.length, 2);
    assert.equal(r.dupCount, 1);
    assert.equal(r.invalidCount, 1);
});
check('同 IP 不同端口不算重复', () => {
    const r = parseLines('1.2.3.4:443\n1.2.3.4:2053');
    assert.equal(r.targets.length, 2);
});

console.log('工作区与筛选（非破坏性）:');
resetStore();
check('setWorkspaceFromText', () => {
    const r = setWorkspaceFromText('1.2.3.4:443\n5.6.7.8:2053\n9.9.9.9:443');
    assert.equal(r.count, 3);
    assert.equal(store.workspace.length, 3);
});
check('筛选不改动 workspace', () => {
    setWorkspaceFilter('port:443');
    assert.equal(visibleWorkspace().length, 2);
    assert.equal(store.workspace.length, 3, '原始数组被筛选破坏了');
});
check('清除筛选回到全量', () => {
    clearWorkspaceFilter();
    assert.equal(visibleWorkspace().length, 3);
});
check('remove 模式', () => {
    setWorkspaceFilter('port:443', 'remove');
    assert.equal(visibleWorkspace().length, 1);
    clearWorkspaceFilter();
});
check('端口范围表达式', () => {
    setWorkspaceFilter('port:2000-3000');
    assert.equal(visibleWorkspace().length, 1);
    clearWorkspaceFilter();
});
check('非法表达式退化为全量而非空', () => {
    setWorkspaceFilter('port:abc');
    assert.equal(filterIsValid(), false);
    assert.equal(visibleWorkspace().length, 3, '无效表达式不应让界面空掉');
    clearWorkspaceFilter();
});
check('空表达式视为合法', () => {
    assert.equal(filterIsValid(), true);
});
check('workspaceStats 反映筛选', () => {
    setWorkspaceFilter('port:443');
    const s = workspaceStats();
    assert.equal(s.visible, 2);
    assert.equal(s.total, 3);
    assert.equal(s.filtered, true);
    clearWorkspaceFilter();
});

console.log('追加与去重:');
check('addToWorkspace 去重', () => {
    const r = addToWorkspace([{ ip: '1.2.3.4', port: 443 }, { ip: '7.7.7.7', port: 80 }]);
    assert.equal(r.added, 1);
    assert.equal(r.dupCount, 1);
    assert.equal(store.workspace.length, 4);
});
check('addToWorkspaceFromText 混合统计', () => {
    const r = addToWorkspaceFromText('8.8.8.8:443\n1.2.3.4:443\n坏行');
    assert.equal(r.added, 1);
    assert.equal(r.dupCount, 1);
    assert.equal(r.invalidCount, 1);
});
check('追加可见行到候选区', () => {
    setWorkspaceFilter('port:443');
    const visibleCount = visibleWorkspace().length;
    const r = appendVisibleToCandidates();
    assert.equal(r.added, visibleCount);
    assert.equal(store.candidates.length, visibleCount);
    clearWorkspaceFilter();
});
check('重复追加不膨胀', () => {
    const before = store.candidates.length;
    setWorkspaceFilter('port:443');
    appendVisibleToCandidates();
    clearWorkspaceFilter();
    assert.equal(store.candidates.length, before);
});
check('candidateTargets 是副本', () => {
    const t = candidateTargets();
    t[0].port = 9999;
    assert.notEqual(store.candidates[0].port, 9999, '返回了内部对象引用');
});
check('clearCandidates', () => { clearCandidates(); assert.equal(store.candidates.length, 0); });

console.log('结果与测速回填:');
resetStore();
check('addResult', () => {
    addResult({ ip: '1.2.3.4', port: 443, tcpLatencyMs: 50 });
    assert.equal(store.results.length, 1);
});
check('updateSpeed 按 ip+port 回填', () => {
    updateSpeed({ ip: '1.2.3.4', port: 443, downloadSpeedKBs: 1234 });
    assert.equal(store.results.length, 1, '不该新增一条');
    assert.equal(store.results[0].downloadSpeedKBs, 1234);
    assert.equal(store.results[0].tcpLatencyMs, 50, '原字段被覆盖了');
});
check('同 IP 不同端口不串号', () => {
    addResult({ ip: '1.2.3.4', port: 2053, tcpLatencyMs: 60 });
    updateSpeed({ ip: '1.2.3.4', port: 2053, downloadSpeedKBs: 999 });
    assert.equal(store.results[0].downloadSpeedKBs, 1234);
    assert.equal(store.results[1].downloadSpeedKBs, 999);
});
check('clearResults', () => { clearResults(); assert.equal(store.results.length, 0); });

console.log('订阅:');
check('变更触发监听，取消后不再触发', () => {
    let n = 0;
    const off = subscribe(() => n++);
    setMode('official');
    assert.equal(n, 1);
    off();
    setMode('proxy');
    assert.equal(n, 1, '取消订阅后仍被调用');
});
check('setMode 同值不重复通知', () => {
    let n = 0;
    const off = subscribe(() => n++);
    setMode('proxy');
    assert.equal(n, 0);
    off();
});

console.log(`\n通过 ${pass} 项`);
