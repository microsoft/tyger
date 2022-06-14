import concurrent.futures
import hashlib
import json
import logging
import os
import shutil
import re

from typing import Dict

from config_types import GadgetronTestCase
from tc_data_generator import TestCaseDataGenerator
from tc_description_generator import TestCaseDescriptionGenerator
from tc_validator import TestCaseValidator
from dependency_downloader import DependencyDownloader


class TestCaseConversionOrchestrator:
    def __init__(self, gadgetron_dir: str, data_dir: str, md5_info: Dict):
        self._data_dir = data_dir
        self._cache_dir = os.path.join(self._data_dir, 'cache')

        self._downloader = DependencyDownloader(self._cache_dir, md5_info)
        self._data_generator = TestCaseDataGenerator(self._data_dir, self._cache_dir, md5_info)
        self._case_generator = TestCaseDescriptionGenerator(self._data_dir)
        self._validator = TestCaseValidator(self._data_generator, self._data_dir)

        self._cases_description = self._read_case_metadata()

    def orchestrate_conversion(self, cases: list[GadgetronTestCase]):
        existing_case_names = [case['name'] for case in self._cases_description['cases']]
        cases_to_validate = [case for case in cases if case.name in existing_case_names]
        cases_to_download = [case for case in cases if case.name not in existing_case_names]

        if cases_to_validate:
            cases_failed_validation = self._validate_existing_cases(cases_to_validate)
            failing_case_names = [case.name for case in cases_failed_validation]

            # Filter out failed cases from the cases description, they get added again later.
            self._cases_description['cases'] = [case for case in self._cases_description['cases'] if case['name'] not in failing_case_names]

            # Add cases which failed to validate to the list of cases to obtain again
            cases_to_download += cases_failed_validation

        if cases_to_download:
            self._downloader.download_dependencies(cases_to_download)
            self._convert_cases(cases_to_download)

        self._write_testdata_json()

    def _read_case_metadata(self):
        cases_description = {'cases': []}
        try:
            with open(os.path.join(self._data_dir, 'testdata.json'), 'r') as cases_description_file:
                cases_description.update(json.load(cases_description_file))
        except FileNotFoundError:
            pass

        return cases_description

    def _validate_existing_cases(self, cases: list[GadgetronTestCase]) -> list[GadgetronTestCase]:
        cases_failed_validation = []

        with concurrent.futures.ProcessPoolExecutor() as executor:
            futures = {executor.submit(
                self._validator.validate, case, self._generate_case_dir(case), self._get_case_metadata(case)): case.name for case in cases}

            for future in concurrent.futures.as_completed(futures):
                try:
                    _ = future.result()
                except BaseException as exc:
                    case_name = futures[future]
                    case = list(filter(lambda case: case.name == case_name, cases))[0]

                    case_dir = self._generate_case_dir(case)
                    if os.path.exists(case_dir):
                        logging.warn(f'Deleting data for {case_name} because validation failed, reason:\n{exc!s}')

                        shutil.rmtree(case_dir)

                    cases_failed_validation.append(case)

        return cases_failed_validation

    def _convert_cases(self, cases: list[GadgetronTestCase]):
        with concurrent.futures.ProcessPoolExecutor() as executor:
            futures = {executor.submit(self._generate_test_case, case, self._generate_case_dir(case)): case.name for case in cases}

            exceptions = []

            for future in concurrent.futures.as_completed(futures):
                try:
                    case_meta = future.result()
                    self._cases_description['cases'].append(case_meta)
                except BaseException as exc:
                    logging.exception(exc)
                    exceptions.append(exc)

            if exceptions:
                raise RuntimeError(f'Failed to convert all test cases')

    def _generate_test_case(self, case: GadgetronTestCase, case_dir: str) -> Dict:
        file_dependencies = self._data_generator.generate(case, case_dir)
        file_dependencies.update(self._case_generator.generate(case, case_dir))

        return {
            'name': case.name,
            'case_file_path': os.path.relpath(os.path.join(case_dir, 'case.yml'), self._data_dir),
            'file_dependencies': file_dependencies
        }

    def _calculate_dependency(self, path: str) -> Dict:
        logging.debug(f'calculating md5sum at {path}...')
        return {path: hashlib.md5(open(path, 'rb').read()).hexdigest()}

    def _generate_case_dir(self, case: GadgetronTestCase) -> str:
        dir_name = case.name.replace('.cfg', '')
        return os.path.join(self._data_dir, dir_name)

    def _get_case_metadata(self, case: GadgetronTestCase) -> Dict:
        return list(filter(lambda case_meta: case_meta['name'] == case.name, self._cases_description['cases']))[0]

    def _write_testdata_json(self):
        self._cases_description['cases'] = sorted(self._cases_description['cases'], key=lambda case: case['name'])

        logging.info('Writing out testdata.json')
        with open(os.path.join(self._data_dir, 'testdata.json'), 'w+') as cases_description_file:
            json.dump(self._cases_description, cases_description_file, indent=4)
