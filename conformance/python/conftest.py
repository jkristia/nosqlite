from case_runner import discover_case_names


def pytest_generate_tests(metafunc):
    if "case_name" in metafunc.fixturenames:
        names = discover_case_names()
        metafunc.parametrize("case_name", names, ids=names)
