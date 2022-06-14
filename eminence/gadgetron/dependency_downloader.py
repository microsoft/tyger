import concurrent.futures
import logging
import os

from typing import Dict

from config_types import GadgetronTestCase
from dl_utils import download_and_validate

GADGETRON_DATA_URL = 'http://gadgetrondata.blob.core.windows.net/gadgetrontestdata/'


class DependencyDownloader:
    def __init__(self, download_dir: str, md5_info: Dict):
        self._download_dir = download_dir
        self._md5_info = md5_info

    def download_dependencies(self, cases: list[GadgetronTestCase]):
        dependency_url_map = self._generate_dependency_url_map(cases)

        with concurrent.futures.ProcessPoolExecutor() as executor:
            futures = {executor.submit(download_and_validate, os.path.join(self._download_dir, dep), url, self._get_md5_entry(dep)):
                       dep for dep, url in dependency_url_map.items()}
            exceptions = []

            for future in concurrent.futures.as_completed(futures):
                try:
                    _ = future.result()
                except BaseException as exc:
                    logging.error(f'Error ocurred while downloading dependency {futures[future]}, reason: {exc!s}')
                    exceptions.append(exc)

            if exceptions:
                raise RuntimeError(f'Failed to download all dependencies')

    def _generate_dependency_url_map(self, cases: list[GadgetronTestCase]):
        dependency_url_map = {}

        def update_dependency(dependency):
            dependency_url_map.update({dependency: GADGETRON_DATA_URL + dependency})

        for case in cases:
            if case.dependency_siemens_config:
                update_dependency(case.dependency_siemens_config.data_file)
            if case.reconstruction_siemens_config:
                update_dependency(case.reconstruction_siemens_config.data_file)
            elif case.reconstruction_copy_config:
                update_dependency(case.reconstruction_copy_config.source)
            if case.reconstruction_test_config:
                for config in case.reconstruction_test_config:
                    update_dependency(config.reference_file)

        return dependency_url_map

    def _get_md5_entry(self, file: str):
        entry = list(filter(lambda info: info['file'] == file, self._md5_info))[0]
        return entry['md5']
