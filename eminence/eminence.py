#!/usr/bin/env python3

import argparse
import os
import sys
from pathlib import Path
import random
import subprocess
from typing import Dict, List, Optional
import yaml

from eminence_tools import get_dependency_image


def ensure_tyger_logged_in():
    try:
        subprocess.check_call(["tyger", "login", "status"])
    except FileNotFoundError:
        print("tyger CLI application not found.")
        sys.exit(1)
    except subprocess.CalledProcessError as exception:
        print("Run `tyger login` before running this script again.")
        sys.exit(exception.returncode)


def run_scanner(infile: Path, outfile: Path, input_uri: str, output_uri: str, session_id: Optional[str] = None, verbose=False):
    image = get_dependency_image("scanner")

    infile_filename = infile.name
    outfile_filename = outfile.name

    infile_folder_str = str(infile.parent.absolute())
    outfile_folder_str = str(outfile.parent.absolute())

    # If we are in devcontainer, we need to get the mounts right
    if "HOST_WORKSPACE_DIR" in os.environ and "CONTAINER_WORKSPACE_DIR" in os.environ:
        infile_folder_str = infile_folder_str.replace(
            os.environ["CONTAINER_WORKSPACE_DIR"],
            os.environ["HOST_WORKSPACE_DIR"])

        outfile_folder_str = outfile_folder_str.replace(
            os.environ["CONTAINER_WORKSPACE_DIR"],
            os.environ["HOST_WORKSPACE_DIR"])

    scanner_cmd = [
        "docker", "run",
        "--network=host",
        "-v", f"{infile_folder_str}:/in",
        "-v", f"{outfile_folder_str}:/out",
        "-u", f"{os.getuid()}:{os.getgid()}",
        image,
        "--input-file", f"/in/{infile_filename}",
        "--input-buffer", input_uri,
        "--output-file", f"/out/{outfile_filename}",
        "--output-buffer", output_uri,
    ]

    if session_id and len(session_id) > 0:
        scanner_cmd = scanner_cmd + ["--session-id", session_id]

    subprocess.check_call(scanner_cmd)


def validate_configuration(config: Dict):
    if not 'input' in config['job']:
        config['job']['input'] = []

    if not 'output' in config['job']:
        config['job']['output'] = []

    if not 'replicas' in config['job']:
        config['job']['replicas'] = 1

    if 'worker' in config:
        if not 'replicas' in config['worker']:
            config['worker']['replicas'] = 1

    # Make sure we don't take in use cases that we don't yet support in eminence
    if len(config['job']['output']) > 1:
        raise Exception("Only one (1) output buffer supported.")

    if len(config['job']['input']) > 1:
        raise Exception("Only one (1) input buffer supported.")

    if len(config['job']['output']) != len(config['job']['input']):
        raise Exception("Number of input and output buffers must be the same")

    return config


def codespec_args(config: Dict):
    args = [
        "--image", config['image'],
        "--max-replicas", str(config['replicas']),
    ]

    res: Dict = config['resources'] if 'resources' in config else {}
    args += (["--cpu", str(res['cpu'])] if 'cpu' in res else [])
    args += (["--memory", str(res['memory'])] if 'memory' in res else [])
    args += (["--gpu", str(res['gpu'])] if 'gpu' in res else [])

    if 'input' in config:
        args += [f"-i={','.join(config['input'])}"]

    if 'output' in config:
        args += [f"-o={','.join(config['output'])}"]

    if 'command' in config and len(config['command']) > 0:
        args += [
            "--command",
            "--"
        ] + config['command']
    else:
        args += [
            "--"
        ]

    args += config['args'] if 'args' in config else []

    return args


if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description='Eminence image reconstruction orchestration')

    parser.add_argument('-f', '--input_file', type=Path, help='Input file', required=False)
    parser.add_argument('-o', '--output_file', type=Path, help='Output file', default=Path("out.h5"))
    parser.add_argument('-r', '--run_file', type=Path, help='Run definition file (YAML)', required=True)
    parser.add_argument('-s', '--session_id', type=str, help='Session ID to pass to scanner process')

    parser.add_argument('-t', '--timeout', type=str,
                        help='How log before the run times out. Specified in a sequence of decimal numbers, each with optional fraction and a unit suffix, such as "300s", "1.5h" or "2h45m". Valid time units are "s", "m", "h')
    parser.add_argument('-v', '--verbose', action=argparse.BooleanOptionalAction, help='Verbose output')

    args = parser.parse_args()

    verbose_arg = ["--verbose"] if args.verbose else []

    def run_tyger(arguments: List[str], timeout_seconds=20):
        try:
            out = subprocess.check_output(["tyger"] + verbose_arg + arguments, timeout=timeout_seconds, universal_newlines=True)
            return out.strip()
        except subprocess.CalledProcessError:
            print("Unable to call tyger CLI")
            raise

    ensure_tyger_logged_in()

    with open(args.run_file, 'r') as runfile:
        config = yaml.safe_load(runfile)['tyger']

    config = validate_configuration(config)

    buffers = {}
    for buffer in [{"name": b, "w": True} for b in config['job']['input']] + [{"name": b, "w": False} for b in config['job']['output']]:
        id = run_tyger(["buffer", "create"])
        access_cmd = ["buffer", "access", id]
        if buffer["w"]:
            access_cmd.append("-w")
        uri = run_tyger(access_cmd)
        buffers[buffer["name"]] = {"id": id, "uri": uri}

    job_codespec_name = f"eminence-codespec-{random.randint(0,1000)}"
    tyger_cmd = ["codespec", "create", job_codespec_name, "--kind", "job"] + codespec_args(config['job'])
    job_codespec_version = run_tyger(tyger_cmd)

    if 'worker' in config:
        worker_codespec_name = f"eminence-codespec-{random.randint(0,1000)}"
        tyger_cmd = ["codespec", "create", worker_codespec_name, "--kind", "worker"] + codespec_args(config['worker'])
        worker_codespec_version = run_tyger(tyger_cmd)
    else:
        worker_codespec_name = None
        worker_codespec_version = None

    run_cmd = [
        "run",
        "create",
        "--codespec", job_codespec_name,
        "--version", str(job_codespec_version),
        "--replicas", str(config['job']['replicas'] if 'replicas' in config['job'] else 1)
    ]

    for k in buffers.keys():
        run_cmd += ["-b", f"{k}={buffers[k]['id']}"]

    if 'cluster' in config:
        run_cmd += ["--cluster", config['cluster']]

    if 'nodepool' in config['job']:
        run_cmd += ["--node-pool", config['job']['nodepool']]

    if 'worker' in config:
        run_cmd += [
            "--worker-codespec", worker_codespec_name,
            "--worker-version", str(worker_codespec_version),
            "--worker-replicas", str(config['worker']['replicas'] if 'replicas' in config['worker'] else 1),
        ]

    if worker_codespec_name is not None and 'nodepool' in config['worker']:
        run_cmd += ["--worker-node-pool", config['worker']['nodepool']]

    run_id = run_tyger(run_cmd + (["--timeout", args.timeout] if args.timeout else []))

    print(f"RunId: {run_id}")

    if 'input' in config['job'] and 'output' in config['job']:
        run_scanner(args.input_file, args.output_file,
                    buffers[config['job']['input'][0]]['uri'], buffers[config['job']['output'][0]]['uri'],
                    args.session_id, args.verbose)
    else:
        print("WARNING: Not running scanner since input/output buffers not requested")
