// 临时校验脚本：确认 store.js 的解析与派生视图行为。
// 用 node 直接跑 ESM，不引入测试框架。
import assert from 'node:assert/strict';
import { store, lineToTarget, targetToLine, parseLines, setMode } from '../web/js/store.js';
import { normalizeIPFormat, smartFilter } from '../web/js/input.js';
import { importCSVText } from '../web/js/input.js';

let pass = 0;
const check = (name, fn) => {
    try { fn(); pass++; console.log(`  ok  ${name}`); }
    catch (e) { console.log(`  FAIL ${name}\n       ${e.message}`); process.exitCode = 1; }
};

console.log('lineToTarget:');
check('正常 IPv4', () => assert.deepEqual(lineToTarget('1.2.3.4:443'), { ip: '1.2.3.4', port: 443 }));
check('裸 IPv4 保持未指定端口', () => assert.deepEqual(lineToTarget('1.2.3.4'), { ip: '1.2.3.4', port: 0 }));
check('中文冒号', () => assert.deepEqual(lineToTarget('1.2.3.4：2053'), { ip: '1.2.3.4', port: 2053 }));
check('空格分隔', () => assert.deepEqual(lineToTarget('1.2.3.4 8443'), { ip: '1.2.3.4', port: 8443 }));
check('IPv6 带括号', () => assert.deepEqual(lineToTarget('[2606:4700::1]:2053'), { ip: '2606:4700::1', port: 2053 }));
check('CSV 首列', () => assert.deepEqual(lineToTarget('1.2.3.4:443,日本,东京'), { ip: '1.2.3.4', port: 443 }));
check('导出 CSV 按字段名导入为 IP:端口#国家-城市', () => {
    const csv = 'IP地址,端口号,出站IP位置,城市(中文),网络延迟\r\n1.2.3.4,443,日本,东京,20 ms';
    assert.equal(importCSVText(csv), '1.2.3.4:443#日本-东京');
    assert.deepEqual(lineToTarget(importCSVText(csv)), { ip: '1.2.3.4', port: 443 });
});
check('CSV 支持英文 ip/port/country/city 表头', () => {
    const csv = 'ip,port,country,city\r\n8.8.8.8,8443,美国,洛杉矶';
    assert.equal(importCSVText(csv), '8.8.8.8:8443#美国-洛杉矶');
});
check('CSV 优先使用中文城市字段', () => {
    const csv = 'IP地址,端口号,出站IP位置,城市,城市(中文)\r\n1.1.1.1,443,日本,Tokyo,东京';
    assert.equal(importCSVText(csv), '1.1.1.1:443#日本-东京');
});
check('CSV IPv6 使用带括号的 IP:Port 格式', () => {
    const csv = 'ip,port,country,city\r\n2606:4700::1,443,日本,东京';
    assert.equal(importCSVText(csv), '[2606:4700::1]:443#日本-东京');
    assert.deepEqual(lineToTarget(importCSVText(csv)), { ip: '2606:4700::1', port: 443 });
});

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
check('未指定端口不补写', () => assert.equal(targetToLine({ ip: '1.2.3.4', port: 0 }), '1.2.3.4'));

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
check('端口筛选可识别带备注的行', () => {
    assert.deepEqual(smartFilter(['1.2.3.4:443#日本', '5.6.7.8:80#美国'], 'port:443'), ['1.2.3.4:443#日本']);
});
check('端口筛选可识别空格分隔的输入行', () => {
    assert.deepEqual(smartFilter(['1.2.3.4 443 日本', '5.6.7.8 9443 美国'], 'port:443'), ['1.2.3.4 443 日本']);
});
check('端口精确匹配不会误中 9443', () => {
    assert.deepEqual(smartFilter(['1.2.3.4 443', '5.6.7.8 9443'], 'port:443'), ['1.2.3.4 443']);
});
check('端口范围与排除条件可组合', () => {
    const lines = ['1.1.1.1 8443 日本', '2.2.2.2 8888 美国', '3.3.3.3 9443 日本'];
    assert.deepEqual(smartFilter(lines, 'port:8000-9000 -country:美国'), ['1.1.1.1 8443 日本']);
});
check('支持 != 与 || 语法', () => {
    const lines = ['1.1.1.1 443 日本', '2.2.2.2 2053 美国', '3.3.3.3 443 美国'];
    assert.deepEqual(smartFilter(lines, 'port:443 country!=美国 || port=2053'), ['1.1.1.1 443 日本', '2.2.2.2 2053 美国']);
});
check('关键词可筛选备注', () => {
    assert.deepEqual(smartFilter(['1.2.3.4:443#日本', '5.6.7.8:443#美国'], '日本'), ['1.2.3.4:443#日本']);
});
check('加入候选时备注被丢弃', () => {
    assert.equal(normalizeIPFormat('1.2.3.4:443#日本'), '1.2.3.4:443');
    assert.deepEqual(parseLines('1.2.3.4:443#日本').targets, [{ ip: '1.2.3.4', port: 443 }]);
});


console.log('模式:');
check('setMode 只接受 proxy/official', () => {
    setMode('official');
    assert.equal(store.mode, 'official');
    setMode('bogus');
    assert.equal(store.mode, 'official', '非法模式不应改变当前模式');
    setMode('proxy');
});

console.log(`\n通过 ${pass} 项`);