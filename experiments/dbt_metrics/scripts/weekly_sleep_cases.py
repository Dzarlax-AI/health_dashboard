"""Boundary inputs for the computeSleepAnalysis comparison on shared briefing captures."""


def weekly_sleep_inputs(index):
    cases = [
        {"Sleep": [], "REM": [1.5]},
        {"Sleep": [0, 0], "Awake": [2], "Deep": [1], "REM": [2, 0]},
        {"Sleep": [0, 7, 8], "Awake": [1, 0, 0], "REM": [0, 2]},
        {"Sleep": [6], "Awake": [7], "Deep": [], "REM": [1.625, 4]},
        {"Sleep": [8, 0, 7, 8, 7, 7, 8, 16], "Deep": [2, 0], "Awake": [0.89], "REM": [1.005]},
        {"Sleep": [7.25], "Awake": [], "Deep": [1.125], "REM": [1.25]},
        {"Sleep": [-2, 8, 0], "Deep": [-1.125], "REM": [0], "Awake": [0.125, 0]},
        {"Sleep": [8, 8], "NightSleep": [4, 6], "Awake": [0, 2], "Deep": [0, 1.25]},
        {"Sleep": [8], "Awake": [0.125], "Deep": [1.005], "REM": [0.125]},
    ]
    return cases[index] if index < len(cases) else {}
