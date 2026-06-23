from app.membership import membership_allows


def test_active_statuses():
    assert membership_allows("creator", None)
    assert membership_allows("administrator", None)
    assert membership_allows("member", None)


def test_left_and_kicked():
    assert not membership_allows("left", None)
    assert not membership_allows("kicked", None)


def test_restricted_depends_on_is_member():
    assert membership_allows("restricted", True)
    assert not membership_allows("restricted", False)
    assert not membership_allows("restricted", None)


def test_unknown_status():
    assert not membership_allows("whatever", None)
