import argparse
import configparser
import json
import logging
import os
import tempfile

from config_factories import GadgetronTestCaseFactory
from tc_conversion_orchestrator import TestCaseConversionOrchestrator
from dl_utils import clone_repo


GADGETRON_DATA_DIR = os.path.join(os.path.dirname(os.path.dirname(__file__)), 'data/gadgetron')


def parse_test_cases(gadgetron_repo_dir, repo_data):
    clone_repo(repo_data['gadgetron']['url'], repo_data['gadgetron']['commit_hash'], gadgetron_repo_dir)
    cases_path = os.path.join(gadgetron_repo_dir, 'test/integration/cases')
    file_list = os.listdir(cases_path)
    cfg_files = filter(lambda file: file.endswith('.cfg'), file_list)
    cases = []

    for file in cfg_files:
        config = configparser.ConfigParser()
        conf = config.read(os.path.join(cases_path, file))

        if conf:
            try:
                cases.append(GadgetronTestCaseFactory.construct(config, file))
            except TypeError as exc:
                logging.warning(f'Ignoring {file}, reason: {exc!s}')
        else:
            raise RuntimeError(f'Failed to parse config file: {file}')

    return cases


def get_md5_info(gadgetron_repo_dir):
    md5_path = os.path.join(gadgetron_repo_dir, 'test/integration/data.json')
    with open(md5_path, 'r') as file:
        return json.load(file)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('-v', '--verbose', action="store_const", const=logging.INFO)
    parser.add_argument('-d', '--debug', action="store_const", const=logging.DEBUG)
    args = parser.parse_args()

    level = logging.WARNING
    if args.debug:
        level = logging.DEBUG
    elif args.verbose:
        level = logging.INFO

    logging.basicConfig(level=level)

    with tempfile.TemporaryDirectory() as gadgetron_repo_dir:
        with open(os.path.join(os.path.dirname(__file__), 'repodata.json'), 'r') as repo_file:
            repo_data = json.load(repo_file)

            cases = parse_test_cases(gadgetron_repo_dir, repo_data)
            supported_cases = list(filter(lambda case:
                                          not case.requirements_config.matlab_support and
                                          not case.requirements_config.julia_support, cases))
            logging.info(f'{len(supported_cases)} compatible test cases found.')

            md5_info = get_md5_info(gadgetron_repo_dir)
            orchestrator = TestCaseConversionOrchestrator(gadgetron_repo_dir, GADGETRON_DATA_DIR, md5_info)
            orchestrator.orchestrate_conversion(supported_cases)


if __name__ == '__main__':
    main()
