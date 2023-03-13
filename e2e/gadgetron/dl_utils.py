import logging
import requests
import time
import hashlib
import os
import shutil
import subprocess
import shlex

from datetime import datetime


def download(url, path):
    logging.info(f'Downloading {url}')
    start_time = datetime.now()

    with requests.get(url, stream=True) as r:
        if not os.path.exists(path):
            os.makedirs(os.path.dirname(path), exist_ok=True)

            with open(path, 'wb') as f:
                shutil.copyfileobj(r.raw, f)

    end_time = datetime.now()
    duration = end_time - start_time
    logging.debug(f'Spent {duration.total_seconds()}s downloading {url}')


def download_with_retry(url, path, retries=5):
    retry = 0
    while(retry < retries):
        try:
            download(url, path)
            break
        except (requests.ConnectionError, requests.Timeout, requests.RequestException) as exc:
            logging.debug(f'Handled error while downloading {url}, retrying...\n{exc!s}')

        retry += 1
        if retry >= retries:
            raise RuntimeError(f'Failed downloading {url} - retry limit exceeded')

        time.sleep(1)


def download_and_validate(path, url, expected_md5):
    if os.path.exists(path):
        try:
            validate_md5(path, expected_md5)
        except ValueError as exc:
            logging.warning(f'Deleting file, reason: {exc!s}')
            os.remove(path)
            download_with_retry(url, path)
            validate_md5(path, expected_md5)
    else:
        download_with_retry(url, path)
        validate_md5(path, expected_md5)


def validate_md5(path, expected_md5):
    actual_md5 = hashlib.md5(open(path, 'rb').read()).hexdigest()

    if not expected_md5 == actual_md5:
        raise ValueError(f'{path}: md5 mismatch, expected {expected_md5} got {actual_md5}')
    else:
        logging.debug(f'{path}: md5 match')


def download_repo(download_url, output_path):
    if os.path.isdir(output_path):
        subprocess.run(f"curl -Ls '{download_url}' | tar -xz --strip-components=1 -C '{output_path}'", shell=True, check=True)
