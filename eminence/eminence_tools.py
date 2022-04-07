import json
import os
from pathlib import Path


def get_dependencies():
    current_dir = Path(os.path.dirname(__file__))
    with open(current_dir/"../dependencies.json", "r") as read_file:
        deps = json.load(read_file)
        return deps['dependencies']


def get_dependency_image(dependency_name: str) -> str:
    deps = [d for d in get_dependencies() if d['name'] == dependency_name]
    if len(deps) != 1:
        raise RuntimeError("Error locating dependency")

    return f"{deps[0]['repository']}:{deps[0]['tag']}"
