#!/usr/bin/env python3
"""Build the minimal Android VPN host with pinned Gradle (no global install)."""
import argparse
import hashlib
import os
from pathlib import Path
import subprocess
import urllib.request
import zipfile

ROOT=Path(__file__).resolve().parents[1]
VERSION='8.13'
SHA256='20f1b1176237254a6fc204d8434196fa11a4cfb387567519c61556e8710aed78'

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--prepare-tools',action='store_true')
    args=parser.parse_args()
    cache=ROOT/'_build/gradle'; cache.mkdir(parents=True,exist_ok=True)
    archive=cache/f'gradle-{VERSION}-bin.zip'
    if not archive.exists() or hashlib.sha256(archive.read_bytes()).hexdigest()!=SHA256:
        pending=archive.with_suffix('.download')
        with urllib.request.urlopen(f'https://services.gradle.org/distributions/gradle-{VERSION}-bin.zip',timeout=60) as source, pending.open('wb') as target:
            while block:=source.read(1024*1024): target.write(block)
        if hashlib.sha256(pending.read_bytes()).hexdigest()!=SHA256: raise RuntimeError('Gradle checksum mismatch')
        pending.replace(archive)
    binary=cache/f'gradle-{VERSION}/bin/gradle'
    if not binary.exists():
        with zipfile.ZipFile(archive) as zipped:
            for entry in zipped.namelist():
                if not (cache/entry).resolve().is_relative_to(cache.resolve()): raise RuntimeError('Unsafe archive path')
            zipped.extractall(cache)
        binary.chmod(0o755)
    if args.prepare_tools:
        print(binary); return
    environment=os.environ.copy()
    if not environment.get('JAVA_HOME'):
        candidate=Path('/opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home')
        if candidate.is_dir(): environment['JAVA_HOME']=str(candidate)
    subprocess.run(['bash',ROOT/'scripts/build-mobile.sh','android'],cwd=ROOT,env=environment,check=True)
    subprocess.run([binary,'--no-daemon','-p',ROOT/'examples/android','assembleDebug'],cwd=ROOT,env=environment,check=True)

if __name__=='__main__': main()
