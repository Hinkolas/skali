#!/usr/bin/env python3
"""Sample skali's observation endpoint: queue depth, workers, source freshness.

Records background load before or between experiments. Uses the remote's
session like measure.py and writes no credentials.
"""
import argparse
import json
from pathlib import Path
import time
import urllib.error

from measure import API, now


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--remote', required=True)
    parser.add_argument('--output', required=True, help='New JSON Lines file')
    parser.add_argument('--interval', type=float, default=5, help='Seconds between samples')
    parser.add_argument('--samples', type=int, default=100)
    args = parser.parse_args()
    api = API(args.remote)
    with Path(args.output).open('x') as output:
        try:
            for index in range(args.samples):
                begin = time.monotonic()
                try:
                    record = {'received_at': now(), 'data': api.get('/v1/system/observation')}
                except (urllib.error.URLError, TimeoutError) as error:
                    # Keep sampling across a daemon restart; the gap is evidence too.
                    record = {'received_at': now(), 'error': type(error).__name__}
                output.write(json.dumps(record, separators=(',', ':')) + '\n')
                output.flush()
                if index + 1 < args.samples:
                    time.sleep(max(0, args.interval - (time.monotonic() - begin)))
        except KeyboardInterrupt:
            pass


if __name__ == '__main__':
    main()
