#!/usr/bin/env python3
"""Проверка CHANGELOG.md на формат Keep a Changelog 1.1.0 (ru) и наличие хешей коммитов.

Запуск из корня репозитория: python3 scripts/check-changelog.py [CHANGELOG.md]
Код выхода 1 и список ошибок, если что-то не так.
"""
import re
import subprocess
import sys

SECTIONS = ['Добавлено', 'Изменено', 'Устарело', 'Удалено', 'Исправлено', 'Безопасность']
VERSION = re.compile(r'^## \[(Unreleased|\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\]( - \d{4}-\d{2}-\d{2}( \[YANKED\])?)?$')
HASH = re.compile(r'`([0-9a-f]{7,40})`')

path = sys.argv[1] if len(sys.argv) > 1 else 'CHANGELOG.md'
lines = open(path, encoding='utf-8').read().splitlines()
errors = []


def err(n, msg):
    errors.append(f'{path}:{n}: {msg}')


if not lines or lines[0] != '# Журнал изменений':
    err(1, 'первая строка должна быть «# Журнал изменений»')
text = '\n'.join(lines)
if 'keepachangelog.com/ru/1.1.0' not in text:
    err(1, 'нет ссылки на https://keepachangelog.com/ru/1.1.0/')

versions, section, order, item_start = [], None, [], None
items = []          # (номер строки, текст пункта)
for n, line in enumerate(lines, 1):
    if line.startswith('## '):
        m = VERSION.match(line)
        if not m:
            err(n, f'заголовок версии не по формату: {line!r}')
        else:
            if m.group(1) != 'Unreleased' and not m.group(2):
                err(n, 'у версии нет даты « - ГГГГ-ММ-ДД»')
            versions.append(m.group(1))
        section, order = None, []
    elif line.startswith('### '):
        name = line[4:].strip()
        if name not in SECTIONS:
            err(n, f'раздел {name!r} не из стандарта: {", ".join(SECTIONS)}')
        elif order and SECTIONS.index(name) <= SECTIONS.index(order[-1]):
            err(n, f'раздел {name!r} не по порядку или повторяется')
        else:
            order.append(name)
        section = name
    elif line.startswith('- '):
        if section is None:
            err(n, 'пункт вне раздела ### ...')
        items.append([n, line])
    elif line.startswith('  ') and items and items[-1][0] < n and not line.startswith('  - '):
        items[-1][1] += ' ' + line.strip()

if not versions or versions[0] != 'Unreleased':
    err(1, 'первым разделом должен быть «## [Unreleased]»')
for v in versions:
    if not re.search(rf'^\[{re.escape(v.lower() if v == "Unreleased" else v)}\]: \S+', text, re.M):
        err(1, f'нет ссылки внизу файла для [{v}]')

known = {}
for n, item in items:
    hashes = HASH.findall(item)
    if not hashes:
        err(n, 'в пункте нет хеша коммита в обратных кавычках')
    if 'Технически:' not in item:
        err(n, 'в пункте нет технических деталей («Технически: ...»)')
    for h in hashes:
        if h not in known:
            known[h] = subprocess.run(['git', 'cat-file', '-e', f'{h}^{{commit}}'],
                                      capture_output=True).returncode == 0
        if not known[h]:
            err(n, f'коммита {h} нет в репозитории')

if errors:
    print('\n'.join(errors))
    sys.exit(1)
print(f'{path}: формат Keep a Changelog 1.1.0, {len(items)} пунктов, {len(known)} коммитов найдено')
