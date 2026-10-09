#!/usr/bin/env python3
"""Check HTTPS, authentication, and PostgreSQL/S3 round trips without load."""
import argparse
import json
from pathlib import Path
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--env-file', default='.env')
    parser.add_argument('--domain')
    parser.add_argument('--read-only', action='store_true', help='Verify existing round-trip data after a restart without writing')
    args = parser.parse_args()
    values = dict(line.split('=', 1) for line in Path(args.env_file).read_text().splitlines()
                  if line.strip() and not line.lstrip().startswith('#'))
    domain = args.domain or values['APP_DOMAIN']
    token = values['BENCH_TOKEN']
    checks = []

    def check(method, path, expected, body=None, authorized=True, content=None):
        headers = {'Authorization': 'Bearer ' + token} if authorized else {}
        request = urllib.request.Request('https://' + domain + path, data=body,
                                         headers=headers, method=method)
        begin = time.monotonic()
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            actual = response.read()
            assert response.status == expected, (method, path, response.status, expected)
            if content is not None:
                assert actual == content, (method, path, 'round trip mismatch')
        checks.append({'method': method, 'path': path, 'status': expected,
                       'duration_ms': round((time.monotonic() - begin) * 1000, 3)})

    check('GET', '/health/ready', 200, content=b'ok\n')
    check('GET', '/records/smoke-check', 401, authorized=False)
    for prefix in ('records', 'objects'):
        payload = ('deployment-timing ' + prefix + '\n').encode()
        if not args.read_only:
            check('PUT', '/' + prefix + '/smoke-check', 204, body=payload)
        check('GET', '/' + prefix + '/smoke-check', 200, content=payload)
        check('GET', '/' + prefix + '/smoke-missing', 404)
    if not args.read_only:
        check('PUT', '/records/smoke-too-large', 413, body=b'x' * (65536 + 1))
    check('GET', '/missing', 404)
    print(json.dumps({'domain': domain, 'read_only': args.read_only, 'checks': checks}, indent=2))


if __name__ == '__main__':
    main()
