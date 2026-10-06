'use strict';

const STORAGE_KEY = 'code-verifier-v1';
const DEFAULT_COMPARE = 'COMPARE_MODE_IGNORE_TRAILING_WHITESPACE';

const EXTENSIONS = {
  '.c': 'c', '.cpp': 'cpp', '.cc': 'cpp', '.cxx': 'cpp', '.go': 'go', '.java': 'java',
  '.js': 'javascript', '.mjs': 'javascript', '.cjs': 'javascript', '.py': 'python', '.rs': 'rust',
};

// every starter solves the same small problem as the default tests, read two numbers and print the sum
const STARTERS = {
  c: '#include <stdio.h>\n\nint main(void) {\n    long long a, b;\n    scanf("%lld %lld", &a, &b);\n    printf("%lld\\n", a + b);\n    return 0;\n}\n',
  cpp: '#include <iostream>\n\nint main() {\n    long long a, b;\n    std::cin >> a >> b;\n    std::cout << a + b << \'\\n\';\n}\n',
  go: 'package main\n\nimport "fmt"\n\nfunc main() {\n\tvar a, b int64\n\tfmt.Scan(&a, &b)\n\tfmt.Println(a + b)\n}\n',
  java: 'import java.util.Scanner;\n\npublic class Main {\n    public static void main(String[] args) {\n        Scanner in = new Scanner(System.in);\n        long a = in.nextLong(), b = in.nextLong();\n        System.out.println(a + b);\n    }\n}\n',
  javascript: "const [a, b] = require('fs').readFileSync(0, 'utf8').trim().split(/\\s+/).map(BigInt);\nconsole.log((a + b).toString());\n",
  python: 'a, b = map(int, input().split())\nprint(a + b)\n',
  rust: 'use std::io::Read;\n\nfn main() {\n    let mut input = String::new();\n    std::io::stdin().read_to_string(&mut input).unwrap();\n    let sum: i64 = input.split_whitespace().map(|x| x.parse::<i64>().unwrap()).sum();\n    println!("{}", sum);\n}\n',
};

const DEFAULT_TESTS = [
  { name: 'small', input: '1 2\n', expected: '3\n' },
  { name: 'negative', input: '-5 5\n', expected: '0\n' },
];

const VERDICTS = {
  VERDICT_ACCEPTED: ['Accepted', 'ok'],
  VERDICT_WRONG_ANSWER: ['Wrong answer', 'bad'],
  VERDICT_TIME_LIMIT_EXCEEDED: ['Time limit', 'limit'],
  VERDICT_MEMORY_LIMIT_EXCEEDED: ['Memory limit', 'limit'],
  VERDICT_OUTPUT_LIMIT_EXCEEDED: ['Output limit', 'limit'],
  VERDICT_RUNTIME_ERROR: ['Runtime error', 'bad'],
  VERDICT_COMPILATION_ERROR: ['Compilation failed', 'bad'],
  VERDICT_INTERNAL_ERROR: ['Internal error', 'bad'],
  VERDICT_SKIPPED: ['Skipped', 'skip'],
};

const $ = (id) => document.getElementById(id);
const el = {
  language: $('language'), run: $('run'), runLabel: document.querySelector('.run-label'),
  shortcut: $('run-shortcut'), limitsButton: $('limits-button'), openFile: $('open-file'),
  notice: $('notice'), code: $('code'), gutter: $('gutter'),
  compile: $('compile'), compileTitle: $('compile-title'), compileOutput: $('compile-output'),
  compileClose: $('compile-close'), strip: $('strip'), summary: $('summary'), tests: $('tests'),
  addTest: $('add-test'), loadTests: $('load-tests'), limits: $('limits'), limitsForm: $('limits-form'),
  timeLimit: $('time-limit'), memoryLimit: $('memory-limit'), failFast: $('fail-fast'),
  limitsReset: $('limits-reset'), fileInput: $('file-input'), testsInput: $('tests-input'),
  tabTests: $('tab-tests'), tabOnce: $('tab-once'), panelTests: $('panel-tests'), panelOnce: $('panel-once'),
  testsMeta: $('tests-meta'), onceSummary: $('once-summary'), onceInput: $('once-input'),
  onceResult: $('once-result'), onceMessage: $('once-message'), onceOutputHead: $('once-output-head'),
  onceOutput: $('once-output'), onceStderrBox: $('once-stderr-box'), onceStderrHead: $('once-stderr-head'),
  onceStderr: $('once-stderr'),
};

let nextId = 1;
const state = loadState();
let languages = [];
let results = [];
let running = null;

/* saving between visits */

function loadState() {
  const fallback = {
    language: 'python',
    tab: 'tests',
    onceInput: '',
    code: {},
    tests: DEFAULT_TESTS.map(withId),
    limits: { timeMs: 0, memoryMb: 0, compare: DEFAULT_COMPARE, failFast: false },
  };
  try {
    const saved = JSON.parse(localStorage.getItem(STORAGE_KEY));
    if (!saved || !Array.isArray(saved.tests)) return fallback;
    return {
      language: saved.language || fallback.language,
      tab: saved.tab === 'once' ? 'once' : 'tests',
      onceInput: typeof saved.onceInput === 'string' ? saved.onceInput : '',
      code: saved.code || {},
      tests: saved.tests.map(withId),
      limits: { ...fallback.limits, ...saved.limits },
    };
  } catch {
    return fallback;
  }
}

let saveTimer = 0;
function saveSoon() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    try {
      const tests = state.tests.map(({ name, input, expected }) => ({ name, input, expected }));
      localStorage.setItem(STORAGE_KEY, JSON.stringify({ ...state, tests }));
    } catch {
      // private windows can refuse storage, the page still works without it
    }
  }, 300);
}

function withId(test) {
  return { id: nextId++, name: test.name || '', input: test.input || '', expected: test.expected || '' };
}

/* notices and summary */

function notice(text, kind = 'info') {
  el.notice.textContent = text;
  el.notice.className = 'notice' + (kind === 'error' ? ' error' : '');
  el.notice.hidden = !text;
}

function setSummary(text, tone = '') {
  el.summary.textContent = text;
  el.summary.className = 'summary' + (tone ? ' ' + tone : '');
}

function idleSummary() {
  const n = state.tests.length;
  setSummary(n === 1 ? '1 test' : `${n} tests`);
}

function formatDuration(ms) {
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)} s` : `${ms} ms`;
}

/* languages */

async function refreshLanguages() {
  try {
    const res = await fetch('/v1/languages');
    if (!res.ok) throw new Error(await errorMessage(res));
    languages = (await res.json()).languages || [];
    renderLanguages();
    updateReadiness();
    if (languages.some((l) => !l.ready)) setTimeout(refreshLanguages, 4000);
  } catch (err) {
    notice("Can't reach the verifier. Make sure it is still running, then reload the page.", 'error');
    setTimeout(refreshLanguages, 4000);
  }
}

function renderLanguages() {
  if (!languages.some((l) => l.id === state.language) && languages.length) {
    state.language = (languages.find((l) => l.ready) || languages[0]).id;
  }
  el.language.replaceChildren(...languages.map((l) => {
    const option = new Option(l.ready ? l.name : `${l.name} (downloading)`, l.id);
    option.selected = l.id === state.language;
    return option;
  }));
  if (!el.code.value) setCode(codeFor(state.language));
}

function currentLanguage() {
  return languages.find((l) => l.id === state.language);
}

function updateReadiness() {
  const lang = currentLanguage();
  if (lang && !lang.ready) {
    notice(`${lang.name} is still downloading. You can write code now and run it once it's ready.`);
  } else if (el.notice.textContent.includes('downloading') || el.notice.textContent.includes("Can't reach")) {
    notice('');
  }
  updateRunButton();
}

function codeFor(id) {
  return state.code[id] ?? STARTERS[id] ?? '';
}

/* editor */

function setCode(text) {
  el.code.value = text;
  renderGutter();
}

let gutterLines = 0;
function renderGutter() {
  const lines = el.code.value.split('\n').length;
  if (lines !== gutterLines) {
    gutterLines = lines;
    el.gutter.textContent = Array.from({ length: lines }, (_, i) => i + 1).join('\n');
  }
  el.gutter.scrollTop = el.code.scrollTop;
}

function indentUnit() {
  return state.language === 'go' ? '\t' : '    ';
}

// insertText keeps the browser's undo history working, setRangeText is only a fallback
function insert(text) {
  if (!document.execCommand('insertText', false, text)) {
    el.code.setRangeText(text, el.code.selectionStart, el.code.selectionEnd, 'end');
    el.code.dispatchEvent(new Event('input'));
  }
}

let escaped = false;

function onEditorKey(event) {
  const area = el.code;
  if (event.key === 'Escape') {
    escaped = true;
    return;
  }
  if (event.key === 'Tab' && !escaped) {
    event.preventDefault();
    const { selectionStart: start, selectionEnd: end, value } = area;
    const unit = indentUnit();
    const multiLine = value.slice(start, end).includes('\n');
    if (!event.shiftKey && !multiLine) {
      insert(unit);
      return;
    }
    const lineStart = value.lastIndexOf('\n', start - 1) + 1;
    const block = value.slice(lineStart, end);
    const changed = block.split('\n').map((line) => {
      if (!event.shiftKey) return unit + line;
      if (line.startsWith('\t')) return line.slice(1);
      return line.replace(/^ {1,4}/, '');
    }).join('\n');
    area.setSelectionRange(lineStart, end);
    insert(changed);
    area.setSelectionRange(lineStart, lineStart + changed.length);
    return;
  }
  escaped = false;
  if (event.key === 'Enter' && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) {
    event.preventDefault();
    const { selectionStart: start, value } = area;
    const lineStart = value.lastIndexOf('\n', start - 1) + 1;
    const line = value.slice(lineStart, start);
    let indent = line.match(/^[\t ]*/)[0];
    if (/[{(\[:]\s*$/.test(line)) indent += indentUnit();
    insert('\n' + indent);
  }
}

/* tests */

function renderTests() {
  el.tests.replaceChildren();
  if (!state.tests.length) {
    const empty = document.createElement('li');
    empty.className = 'empty';
    empty.textContent = 'No tests yet. Add one, or load a tests.json file or a folder of .in and .out files.';
    el.tests.append(empty);
  }
  state.tests.forEach((test, i) => el.tests.append(testRow(test, i)));
  renderStrip();
  idleSummary();
}

function testRow(test, index) {
  const li = document.createElement('li');
  li.className = 'test';
  li.dataset.id = test.id;

  const head = document.createElement('div');
  head.className = 'test-head';

  const toggle = document.createElement('button');
  toggle.type = 'button';
  toggle.className = 'test-toggle';
  toggle.textContent = index + 1;
  toggle.setAttribute('aria-expanded', 'true');
  toggle.setAttribute('aria-label', `Show or hide test ${index + 1}`);

  const name = document.createElement('input');
  name.className = 'test-name';
  name.value = test.name;
  name.placeholder = 'Untitled';
  name.setAttribute('aria-label', `Name of test ${index + 1}`);
  name.addEventListener('input', () => { test.name = name.value; saveSoon(); });

  const verdict = document.createElement('span');
  verdict.className = 'verdict';
  const duration = document.createElement('span');
  duration.className = 'duration';

  const remove = document.createElement('button');
  remove.type = 'button';
  remove.className = 'remove';
  remove.textContent = '×';
  remove.setAttribute('aria-label', `Remove test ${index + 1}`);
  remove.addEventListener('click', () => {
    state.tests.splice(state.tests.indexOf(test), 1);
    results = [];
    saveSoon();
    renderTests();
  });

  head.append(toggle, name, verdict, duration, remove);
  // the whole header toggles, except the parts that do something else
  head.addEventListener('click', (event) => {
    if (event.target.closest('input, .remove')) return;
    setCollapsed(li, !li.classList.contains('collapsed'));
  });

  const io = document.createElement('div');
  io.className = 'io';
  io.append(
    ioField('Input', test.input, (v) => { test.input = v; }),
    ioField('Expected output', test.expected, (v) => { test.expected = v; }),
  );

  const details = document.createElement('div');
  details.className = 'details';
  details.hidden = true;

  li.append(head, io, details);
  return li;
}

let fieldCount = 0;

function ioField(label, value, onChange) {
  const wrap = document.createElement('div');
  wrap.className = 'box';
  const head = document.createElement('div');
  head.className = 'box-head';
  const area = document.createElement('textarea');
  area.id = `field-${++fieldCount}`;
  const caption = document.createElement('label');
  caption.htmlFor = area.id;
  caption.textContent = label;
  head.append(caption);
  area.spellcheck = false;
  area.value = value;
  area.rows = Math.min(Math.max(value.split('\n').length, 2), 8);
  area.addEventListener('input', () => {
    onChange(area.value);
    area.rows = Math.min(Math.max(area.value.split('\n').length, 2), 8);
    saveSoon();
  });
  wrap.append(head, area);
  return wrap;
}

function setCollapsed(row, collapsed) {
  row.classList.toggle('collapsed', collapsed);
  row.querySelector('.test-toggle').setAttribute('aria-expanded', String(!collapsed));
}

function rowFor(index) {
  const test = state.tests[index];
  return test && el.tests.querySelector(`[data-id="${test.id}"]`);
}

function isFailure(verdict) {
  return verdict !== 'VERDICT_ACCEPTED' && verdict !== 'VERDICT_SKIPPED';
}

function showResult(index, result) {
  const row = rowFor(index);
  if (!row) return;
  const [label, tone] = VERDICTS[result.verdict] || ['Unknown', 'bad'];
  const verdict = row.querySelector('.verdict');
  verdict.textContent = label;
  verdict.className = `verdict ${tone}`;
  row.querySelector('.duration').textContent = result.verdict === 'VERDICT_SKIPPED' ? '' : formatDuration(result.duration_ms);

  const details = row.querySelector('.details');
  details.replaceChildren();
  const failed = isFailure(result.verdict);
  details.hidden = !failed;
  // passing tests fold away so the failures are what you see
  setCollapsed(row, !failed);
  if (!failed) return;

  if (result.message) {
    const message = document.createElement('p');
    message.className = 'message';
    message.textContent = sentence(result.message);
    details.append(message);
  }
  // your output sits under the expected output so the two can be compared
  const io = document.createElement('div');
  io.className = 'io';
  if (result.stderr) {
    const err = outputBlock('Error output', result.stderr, result.stderr_truncated);
    err.classList.add('stderr');
    io.append(err);
  }
  const yours = outputBlock('Your output', result.stdout, result.stdout_truncated, '(nothing)');
  yours.classList.add('yours');
  io.append(yours);
  details.append(io);
}

function outputBlock(label, text, truncated, emptyText = '') {
  const wrap = document.createElement('div');
  wrap.className = 'box';
  const caption = document.createElement('div');
  caption.className = 'box-head';
  caption.textContent = label;
  if (truncated) {
    const cut = document.createElement('span');
    cut.className = 'truncated';
    cut.textContent = ', cut off';
    caption.append(cut);
  }
  const pre = document.createElement('pre');
  pre.textContent = text || emptyText;
  wrap.append(caption, pre);
  return wrap;
}

function sentence(text) {
  return text.charAt(0).toUpperCase() + text.slice(1) + (/[.!?]$/.test(text) ? '' : '.');
}

function clearResults() {
  results = [];
  for (const row of el.tests.querySelectorAll('.test')) {
    row.querySelector('.verdict').textContent = '';
    row.querySelector('.verdict').className = 'verdict';
    row.querySelector('.duration').textContent = '';
    row.querySelector('.details').hidden = true;
  }
}

/* the strip of squares, one per test */

function renderStrip(activeIndex = -1) {
  el.strip.replaceChildren(...state.tests.map((test, i) => {
    const cell = document.createElement('button');
    cell.type = 'button';
    cell.className = 'cell';
    const result = results[i];
    let label = 'not run';
    if (result) {
      const [name, tone] = VERDICTS[result.verdict] || ['Unknown', 'bad'];
      cell.classList.add(tone);
      label = name.toLowerCase();
    } else if (i === activeIndex) {
      cell.classList.add('active');
      label = 'running';
    }
    cell.setAttribute('aria-label', `Test ${i + 1}, ${label}`);
    cell.title = `Test ${i + 1}${test.name ? ', ' + test.name : ''}, ${label}`;
    cell.addEventListener('click', () => focusTest(i));
    return cell;
  }));
  const done = results.filter(Boolean).length;
  el.strip.setAttribute('aria-label', done ? `${done} of ${state.tests.length} tests finished` : 'No results yet');
}

function focusTest(index) {
  const row = rowFor(index);
  if (!row) return;
  row.scrollIntoView({ block: 'nearest', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
  row.classList.remove('flash');
  void row.offsetWidth;
  row.classList.add('flash');
}

/* compile panel */

function showCompile(compile) {
  const output = (compile.output || '').trim();
  if (compile.success && !output) {
    el.compile.hidden = true;
    return;
  }
  el.compile.hidden = false;
  el.compile.classList.toggle('failed', !compile.success);
  el.compileTitle.textContent = compile.success ? 'Compiler warnings' : 'Compilation failed';
  el.compileOutput.textContent = output || 'The compiler gave no details.';
}

/* running */

function updateRunButton() {
  const lang = currentLanguage();
  el.run.classList.toggle('running', Boolean(running));
  el.runLabel.textContent = running ? 'Stop' : 'Run';
  el.run.disabled = !running && Boolean(lang && !lang.ready);
  el.addTest.disabled = el.loadTests.disabled = Boolean(running);
  for (const button of el.tests.querySelectorAll('.remove')) button.disabled = Boolean(running);
}

/* tabs */

function setTab(tab, focus = false) {
  state.tab = tab;
  const once = tab === 'once';
  el.tabTests.setAttribute('aria-selected', String(!once));
  el.tabOnce.setAttribute('aria-selected', String(once));
  el.tabTests.tabIndex = once ? -1 : 0;
  el.tabOnce.tabIndex = once ? 0 : -1;
  el.panelTests.hidden = once;
  el.testsMeta.hidden = once;
  el.panelOnce.hidden = !once;
  el.onceSummary.hidden = !once;
  if (focus) (once ? el.tabOnce : el.tabTests).focus();
  saveSoon();
}

/* run once */

function setOnceSummary(text, tone = '') {
  el.onceSummary.textContent = text;
  el.onceSummary.className = 'summary' + (tone ? ' ' + tone : '');
}

function setOutputHead(head, label, truncated) {
  head.textContent = label;
  if (truncated) {
    const cut = document.createElement('span');
    cut.className = 'truncated';
    cut.textContent = ', cut off';
    head.append(cut);
  }
}

function showOnce(result) {
  // there is nothing to compare against, so accepted and wrong answer both just mean it finished
  const finished = result.verdict === 'VERDICT_ACCEPTED' || result.verdict === 'VERDICT_WRONG_ANSWER';
  if (finished) {
    setOnceSummary(`Finished in ${formatDuration(result.duration_ms)}`);
    el.onceMessage.hidden = true;
  } else {
    const [label, tone] = VERDICTS[result.verdict] || ['Unknown', 'bad'];
    setOnceSummary(`${label} after ${formatDuration(result.duration_ms)}`, tone);
    el.onceMessage.textContent = sentence(result.message || 'stopped');
    el.onceMessage.hidden = false;
  }
  setOutputHead(el.onceOutputHead, 'Output', result.stdout_truncated);
  el.onceOutput.textContent = result.stdout || '(nothing)';
  el.onceStderrBox.hidden = !result.stderr;
  setOutputHead(el.onceStderrHead, 'Error output', result.stderr_truncated);
  el.onceStderr.textContent = result.stderr || '';
  el.onceResult.hidden = false;
}

async function runOnce() {
  const { limits } = state;
  const request = {
    language: state.language,
    source_code: el.code.value,
    test_cases: [{ name: 'run once', input: el.onceInput.value, expected_output: '' }],
    time_limit_ms: limits.timeMs || 0,
    memory_limit_mb: limits.memoryMb || 0,
  };

  el.compile.hidden = true;
  el.onceResult.hidden = true;
  running = new AbortController();
  updateRunButton();
  setOnceSummary('Compiling');
  let finished = false;

  try {
    const res = await fetch('/v1/verify?stream=true', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(request),
      signal: running.signal,
    });
    if (!res.ok) throw new Error(await errorMessage(res));
    await readLines(res.body, (event) => {
      if (event.compile) {
        showCompile(event.compile);
        setOnceSummary(event.compile.success ? 'Running' : 'Compilation failed', event.compile.success ? '' : 'bad');
      } else if (event.test_result) {
        if (event.test_result.verdict !== 'VERDICT_SKIPPED') showOnce(event.test_result);
      } else if (event.summary) {
        finished = true;
      } else if (event.error) {
        throw new Error(sentence(event.error.message));
      }
    });
    if (!finished) throw new Error('The verifier stopped before sending a result. Check the terminal it runs in.');
  } catch (err) {
    if (err.name === 'AbortError') {
      setOnceSummary('Stopped');
    } else if (err instanceof TypeError) {
      notice("Can't reach the verifier. Make sure it is still running.", 'error');
      setOnceSummary('');
    } else {
      notice(err.message, 'error');
      setOnceSummary('');
    }
  } finally {
    running = null;
    updateRunButton();
  }
}

async function run() {
  if (running) {
    running.abort();
    return;
  }
  state.code[state.language] = el.code.value;
  saveSoon();
  if (!el.code.value.trim()) {
    notice('Write or open some code first.');
    return;
  }
  if (state.tab === 'once') {
    notice('');
    runOnce();
    return;
  }
  if (!state.tests.length) {
    notice('Add at least one test first.');
    return;
  }
  notice('');

  const { limits } = state;
  const request = {
    language: state.language,
    source_code: el.code.value,
    test_cases: state.tests.map((t) => ({ name: t.name, input: t.input, expected_output: t.expected })),
    time_limit_ms: limits.timeMs || 0,
    memory_limit_mb: limits.memoryMb || 0,
    compare_mode: limits.compare,
    stop_on_first_failure: limits.failFast,
  };

  clearResults();
  el.compile.hidden = true;
  running = new AbortController();
  updateRunButton();
  renderStrip();
  setSummary('Compiling');
  let finished = false;

  try {
    const res = await fetch('/v1/verify?stream=true', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(request),
      signal: running.signal,
    });
    if (!res.ok) throw new Error(await errorMessage(res));
    await readLines(res.body, (event) => {
      if (event.compile) {
        showCompile(event.compile);
        if (event.compile.success) {
          renderStrip(0);
          setSummary(`Running test 1 of ${state.tests.length}`);
        }
      } else if (event.test_result) {
        const result = event.test_result;
        results[result.index] = result;
        showResult(result.index, result);
        const next = result.index + 1;
        renderStrip(next < state.tests.length && result.verdict !== 'VERDICT_SKIPPED' ? next : -1);
        if (next < state.tests.length && !el.compile.classList.contains('failed')) {
          setSummary(`Running test ${next + 1} of ${state.tests.length}`);
        }
      } else if (event.summary) {
        finished = true;
        finish(event.summary);
      } else if (event.error) {
        throw new Error(sentence(event.error.message));
      }
    });
    if (!finished) throw new Error('The verifier stopped before sending a result. Check the terminal it runs in.');
  } catch (err) {
    if (err.name === 'AbortError') {
      setSummary('Stopped');
      renderStrip();
    } else if (err instanceof TypeError) {
      notice("Can't reach the verifier. Make sure it is still running.", 'error');
      idleSummary();
    } else {
      notice(err.message, 'error');
      idleSummary();
      renderStrip();
    }
  } finally {
    running = null;
    updateRunButton();
  }
}

function finish(summary) {
  const [label, tone] = VERDICTS[summary.verdict] || ['Unknown', 'bad'];
  renderStrip();
  if (summary.verdict === 'VERDICT_COMPILATION_ERROR') {
    setSummary(label, 'bad');
    return;
  }
  setSummary(`${label}, ${summary.passed} of ${summary.total} passed in ${formatDuration(summary.duration_ms)}`, tone);
  const firstFailure = results.findIndex((r) => r && isFailure(r.verdict));
  if (firstFailure >= 0) focusTest(firstFailure);
}

async function readLines(stream, onEvent) {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let newline;
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const line = buffer.slice(0, newline).trim();
      buffer = buffer.slice(newline + 1);
      if (line) onEvent(JSON.parse(line));
    }
  }
  buffer += decoder.decode();
  if (buffer.trim()) onEvent(JSON.parse(buffer));
}

async function errorMessage(res) {
  try {
    const body = await res.json();
    return sentence(body.error.message);
  } catch {
    return `The verifier answered with status ${res.status}.`;
  }
}

/* limits dialog */

function limitsLabel() {
  const { timeMs, memoryMb, compare, failFast } = state.limits;
  const custom = timeMs || memoryMb || compare !== DEFAULT_COMPARE || failFast;
  if (!custom) return 'Limits';
  const parts = [];
  if (timeMs) parts.push(formatDuration(timeMs));
  if (memoryMb) parts.push(`${memoryMb} MB`);
  return parts.length ? `Limits, ${parts.join(', ')}` : 'Limits, custom';
}

function openLimits() {
  const { timeMs, memoryMb, compare, failFast } = state.limits;
  el.timeLimit.value = timeMs || '';
  el.timeLimit.placeholder = 'Server default';
  el.memoryLimit.value = memoryMb || '';
  el.memoryLimit.placeholder = 'Server default';
  el.limitsForm.elements.compare.value = compare;
  el.failFast.checked = failFast;
  el.limits.showModal();
}

function saveLimits(event) {
  event.preventDefault();
  state.limits = {
    timeMs: Math.max(0, Math.round(Number(el.timeLimit.value) || 0)),
    memoryMb: Math.max(0, Math.round(Number(el.memoryLimit.value) || 0)),
    compare: el.limitsForm.elements.compare.value || DEFAULT_COMPARE,
    failFast: el.failFast.checked,
  };
  el.limitsButton.textContent = limitsLabel();
  saveSoon();
  el.limits.close();
}

/* files */

function openSourceFile(file) {
  const dot = file.name.lastIndexOf('.');
  const id = EXTENSIONS[dot >= 0 ? file.name.slice(dot).toLowerCase() : ''];
  file.text().then((text) => {
    if (id && languages.some((l) => l.id === id)) {
      state.code[state.language] = el.code.value;
      state.language = id;
      el.language.value = id;
      updateReadiness();
    } else if (id) {
      notice(`${file.name} looks like ${id}, which this server doesn't have enabled.`, 'error');
      return;
    }
    setCode(text);
    state.code[state.language] = text;
    saveSoon();
  });
}

async function loadTestFiles(files) {
  try {
    const list = [...files];
    let tests;
    const json = list.find((f) => f.name.toLowerCase().endsWith('.json'));
    if (json) {
      const data = JSON.parse(await json.text());
      const cases = Array.isArray(data) ? data : data.test_cases;
      if (!Array.isArray(cases)) throw new Error(`${json.name} needs an array of tests, or an object with a test_cases array.`);
      tests = cases.map((c) => ({ name: c.name || '', input: c.input || '', expected: c.expected_output ?? c.expectedOutput ?? '' }));
    } else {
      tests = await pairFiles(list);
    }
    if (!tests.length) throw new Error('No tests found in the selected files.');
    state.tests = tests.map(withId);
    results = [];
    saveSoon();
    renderTests();
    notice(`Loaded ${tests.length} ${tests.length === 1 ? 'test' : 'tests'}.`);
  } catch (err) {
    notice(err instanceof SyntaxError ? "That JSON file couldn't be read." : err.message, 'error');
  }
}

// pairs NAME.in with NAME.out or NAME.ans, in natural order so 2 comes before 10
async function pairFiles(files) {
  const byName = new Map(files.map((f) => [f.name, f]));
  const inputs = files.filter((f) => f.name.endsWith('.in'));
  inputs.sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
  const tests = [];
  for (const input of inputs) {
    const base = input.name.slice(0, -3);
    const answer = byName.get(base + '.out') || byName.get(base + '.ans');
    if (!answer) throw new Error(`${input.name} has no matching ${base}.out or ${base}.ans file.`);
    tests.push({ name: base, input: await input.text(), expected: await answer.text() });
  }
  return tests;
}

/* wiring */

function init() {
  const mac = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
  el.shortcut.textContent = mac ? '⌘ ↵' : 'Ctrl ↵';
  el.limitsButton.textContent = limitsLabel();

  setCode(codeFor(state.language));
  renderTests();

  el.code.addEventListener('input', () => {
    renderGutter();
    state.code[state.language] = el.code.value;
    saveSoon();
  });
  el.code.addEventListener('scroll', () => { el.gutter.scrollTop = el.code.scrollTop; });
  el.code.addEventListener('keydown', onEditorKey);

  el.language.addEventListener('change', () => {
    state.code[state.language] = el.code.value;
    state.language = el.language.value;
    setCode(codeFor(state.language));
    el.compile.hidden = true;
    updateReadiness();
    saveSoon();
  });

  el.run.addEventListener('click', run);
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      if (!el.run.disabled) run();
    }
  });

  el.addTest.addEventListener('click', () => {
    state.tests.push(withId({}));
    results = [];
    saveSoon();
    renderTests();
    el.tests.lastElementChild.querySelector('textarea').focus();
  });

  el.loadTests.addEventListener('click', () => el.testsInput.click());
  el.testsInput.addEventListener('change', () => {
    if (el.testsInput.files.length) loadTestFiles(el.testsInput.files);
    el.testsInput.value = '';
  });

  el.openFile.addEventListener('click', () => el.fileInput.click());
  el.fileInput.addEventListener('change', () => {
    if (el.fileInput.files[0]) openSourceFile(el.fileInput.files[0]);
    el.fileInput.value = '';
  });

  el.compileClose.addEventListener('click', () => { el.compile.hidden = true; });

  el.tabTests.addEventListener('click', () => setTab('tests'));
  el.tabOnce.addEventListener('click', () => setTab('once'));
  // left and right arrows move between tabs, like any other tab list
  for (const tab of [el.tabTests, el.tabOnce]) {
    tab.addEventListener('keydown', (event) => {
      if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
        event.preventDefault();
        setTab(state.tab === 'once' ? 'tests' : 'once', true);
      }
    });
  }
  el.onceInput.value = state.onceInput;
  el.onceInput.addEventListener('input', () => {
    state.onceInput = el.onceInput.value;
    saveSoon();
  });
  setTab(state.tab);

  el.limitsButton.addEventListener('click', openLimits);
  el.limitsForm.addEventListener('submit', saveLimits);
  el.limitsReset.addEventListener('click', () => {
    el.timeLimit.value = '';
    el.memoryLimit.value = '';
    el.limitsForm.elements.compare.value = DEFAULT_COMPARE;
    el.failFast.checked = false;
  });

  refreshLanguages();
}

init();
