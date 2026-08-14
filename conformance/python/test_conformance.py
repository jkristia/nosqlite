from case_runner import run_case


def test_case(case_name: str) -> None:
    result = run_case(case_name)
    assert result.got == result.expected
    assert result.got_docs == result.expected_docs
