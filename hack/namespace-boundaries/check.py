"""Run namespace boundaries through the original api-syncagent v0.7.0 code."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import yaml

REVISION = 'd9e14d998a374d81fb73254069b9be0f3d02cd9f'


def check(documents, upstream=None):
    published = [obj for obj in documents if obj.get('kind') == 'PublishedResource']
    if not published:
        raise ValueError('No PublishedResources found for native boundary checks')
    with tempfile.TemporaryDirectory(prefix='msp-native-boundaries-') as temporary:
        root = Path(temporary).resolve()
        source = root / 'upstream'
        source.mkdir()
        upstream = upstream or os.environ.get('MSP_SYNCAGENT_SOURCE')
        if upstream:
            archive = subprocess.Popen(['git', '-C', upstream, 'archive', REVISION], stdout=subprocess.PIPE)
            subprocess.run(['tar', '-x', '-C', str(source)], stdin=archive.stdout, check=True)
            archive.stdout.close()
            if archive.wait():
                raise RuntimeError('Cannot read the pinned api-syncagent revision')
        else:
            subprocess.run(['git', 'init', '-q', str(source)], check=True)
            subprocess.run(['git', '-C', str(source), 'fetch', '-q', '--depth', '1',
                            'https://github.com/kcp-dev/api-syncagent.git', REVISION], check=True)
            subprocess.run(['git', '-C', str(source), 'checkout', '-q', '--detach', 'FETCH_HEAD'], check=True)
        inputs = root / 'publishedresources.json'
        inputs.write_text(json.dumps(published))
        overlay = root / 'overlay.json'
        test = Path(__file__).resolve().with_name('sync_boundaries_test.go.txt')
        overlay.write_text(json.dumps({'Replace': {
            str(source / 'internal/sync/msp_namespace_boundaries_test.go'): str(test)}}))
        environment = dict(os.environ, MSP_BOUNDARY_INPUT=str(inputs), GOMAXPROCS='2')
        subprocess.run(['go', 'test', '-overlay=' + str(overlay), './internal/sync',
                        '-run', '^TestMSPNamespaceBoundaries$', '-count=1', '-v'],
                       cwd=source, env=environment, check=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--chart', type=Path)
    parser.add_argument('--artifacts', type=Path)
    parser.add_argument('--upstream', help='A Git checkout containing the exact v0.7.0 revision')
    args = parser.parse_args()
    if bool(args.chart) == bool(args.artifacts):
        parser.error('Choose exactly one of --chart or --artifacts')
    if args.chart:
        raw = subprocess.check_output(['helm', 'template', 'boundary-test', str(args.chart),
                                       '--set', 'syncAgentOperator.enabled=false'], text=True)
        documents = [obj for obj in yaml.safe_load_all(raw) if obj]
    else:
        documents = [obj for path in sorted(args.artifacts.glob('*.yaml'))
                     if not path.name.endswith('-values.yaml')
                     for obj in yaml.safe_load_all(path.read_text()) if isinstance(obj, dict) and 'kind' in obj]
    check(documents, args.upstream)


if __name__ == '__main__':
    main()
