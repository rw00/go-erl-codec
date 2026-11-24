#!/usr/bin/env escript
%%! -sname etf_verify

main(_) ->
    Bin = find_bin(),
    Cases = [
        %% Scalar numbers
        {"small_int_0", "scalar_int64", 0, []},
        {"small_int_255", "scalar_int64", 255, []},
        {"int32_neg", "scalar_int64", -2147483648, []},
        {"int32_pos", "scalar_int64", 2147483647, []},
        {"int64_min", "scalar_int64", -9223372036854775808, []},
        {"int64_max", "scalar_int64", 9223372036854775807, []},
        {"uint64_max", "scalar_uint64", 18446744073709551615, []},
        {"small_big_pos", "scalar_bigint", 1234567890123456789012345678901234567890, []},
        {"small_big_neg", "scalar_bigint", -9876543210987654321098765432109876543210, []},
        {"large_big_pos", "scalar_bigint", (1 bsl 2200) + 12345, []},
        {"large_big_neg", "scalar_bigint", -((1 bsl 2200) + 12345), []},

        %% Floats (NEW_FLOAT_EXT)
        {"float_pi", "scalar_float", 3.141592653589793, []},
        {"float_neg_zero", "scalar_float", -0.0, []},
        {"float_tiny", "scalar_float", 1.0e-300, []},

        %% Atoms & Booleans (UTF-8, >255-byte UTF-8)
        {"atom_ascii", "scalar_atom", hello_erlang, []},
        {"atom_empty", "scalar_atom", '', []},
        {"atom_unicode", "scalar_atom", 'héllo_🚀_世界', []},
        {"atom_utf8_over_255_bytes", "scalar_atom", list_to_atom(lists:duplicate(200, 233)), []},
        {"bool_true", "scalar_bool", true, []},
        {"bool_false", "scalar_bool", false, []},

        %% Binaries & Bitstrings
        {"binary_empty", "scalar_binary", <<>>, []},
        {"binary_sigil", "scalar_binary", ~"hello world 🚀", []},
        {"binary_raw_bytes", "scalar_binary", <<0, 1, 127, 128, 255>>, []},
        {"bitstring_3bits", "scalar_bitstring", <<16#FF, 5:3>>, []},

        %% Lists & Charlists
        {"list_empty", "scalar_int_slice", [], []},
        {"list_string_ext_bytes", "scalar_int_slice", [1, 2, 3, 255], []},
        {"list_ints_above_255", "scalar_int_slice", [1, 256, -50, 100000], []},
        {"charlist_latin1", "scalar_charlist", "héllo world", []},
        {"charlist_unicode", "scalar_charlist", "hello 世界 🚀", []},
        {"improper_list", "any", [1, 2 | tail_atom], []},

        %% Payment request with mixed atom/binary keys
        {"payment_request_struct", "payment_request",
            #{ user_id => ~"abc123", ~"user_id" => ~"abc123", amount => 41.5, currency => ~"EUR" }, []},
        {"payment_request_any", "any",
            #{ user_id => ~"abc123", ~"user_id" => ~"abc123", amount => 41.5, currency => ~"EUR" }, []},

        %% Arbitrary Erlang map keys (atom, binary, int, float, list, nested map)
        {"arbitrary_map_keys", "any",
            #{
                user_id => ~"abc123",
                ~"user_id" => ~"legacy-abc123",
                1 => tier_one,
                1.0 => rate_one,
                [1, 2] => bucket_key_val,
                #{currency => ~"EUR"} => ~"eu_ledger"
            }, []},

        %% Deep nested order request with typed lists, typed maps, mixed key types, nil pointer
        {"order_request_full", "order_request",
            #{
                order_id => 9007199254740993,
                user_id => ~"abc123",
                capture => true,
                tags => [vip, recurring, express],
                line_amounts => [10, 20.5, 11],
                installments => [[2000, 1], [2150, 2]],
                metadata => #{~"region" => [eu, de], ~"tier" => [gold]},
                shipping => #{city => ~"Berlin", postal_code => ~"10115"},
                opt_note => undefined,
                extra => #{
                    "legacy_ref" => "ref-héllo",
                    100 => ~"enterprise",
                    2.5 => standard_fee,
                    true => ~"enabled"
                }
            }, []},

        %% Numeric type coercion round-trip
        {"coercion_struct", "coercion_struct",
            #{
                a => 127,
                b => -100000,
                c => 9223372036854775807,
                d => 18446744073709551615,
                e => 123456,
                f => 42.0
            }, []},

        %% Custom Marshaler/Unmarshaler + RawTerm passthrough (including Erlang tuple!)
        {"custom_struct_and_raw_tuple", "custom_struct",
            #{
                items => ~"sku_1|sku_2|sku_3",
                raw => {ok, 42, ~"gateway_approved"}
            }, []},

        %% Tuples (empty, small, large >255, nested, tuple map keys)
        {"tuple_empty", "any", {}, []},
        {"tuple_small_nested", "any", {ok, 42, ~"hello", {nested, true, [1, 2]}}, []},
        {"tuple_large_300", "any", list_to_tuple(lists:seq(1, 300)), []},
        {"tuple_map_keys", "any", #{{ok, 1} => ~"first", {err, ~"bad"} => 2}, []},

        %% Erlang calendar:datetime() and custom Marshaler/Unmarshaler tuple struct
        {"datetime_and_custom_tuple", "datetime_struct",
            #{
                created_at => {{2026, 11, 24}, {19, 50, 41}},
                leap_day => {{2024, 2, 29}, {23, 59, 59}},
                opt_at => undefined,
                custom_tuple => {ok, 99, ~"approved"}
            }, []},

        %% Zlib compressed round-trip (Erlang compressed -> Go decode -> Go compressed encode -> Erlang)
        {"compressed_roundtrip", "compressed",
            #{
                payload => binary:copy(~"payment-ledger-compressed-", 100),
                amounts => lists:seq(1, 300),
                user_id => ~"abc123",
                ~"user_id" => ~"abc123"
            }, [compressed]}
    ],

    Results = [run_case(Bin, Case) || Case <- Cases] ++ run_ignored_unsupported_cases(Bin),
    Passed = length([ok || ok <- Results]),
    Total = length(Results),
    io:format("~nInterop verification: ~p/~p passed.~n", [Passed, Total]),
    case Passed =:= Total of
        true -> halt(0);
        false -> halt(1)
    end.

run_ignored_unsupported_cases(Bin) ->
    Port = open_port({spawn, "cat"}, []),
    Term = #{
        id => 42,
        name => ~"worker",
        pid => self(),
        ref => make_ref(),
        port => Port,
        export => fun lists:reverse/1,
        closure => fun(X) -> X + 1 end,
        nested => #{owner => self(), refs => [make_ref()], tup => {ok, self()}},
        self() => ~"pid_as_key",
        {tag, self()} => make_ref()
    },
    Expected = #{id => 42, name => ~"worker"},
    Res = [
        run_expected_case(Bin, "ignore_unsupported_otp27", "ignore_unsupported", Term, Expected, []),
        run_expected_case(Bin, "ignore_unsupported_compressed", "ignore_unsupported", Term, Expected, [compressed])
    ],
    port_close(Port),
    Res.

run_expected_case(Bin, Name, Mode, Term, Expected, Opts) ->
    InFile = "/tmp/etf_in_" ++ Name ++ ".bin",
    OutFile = "/tmp/etf_out_" ++ Name ++ ".bin",
    Encoded = erlang:term_to_binary(Term, Opts),
    ok = file:write_file(InFile, Encoded),
    Cmd = lists:flatten(io_lib:format("~s ~s ~s ~s", [Bin, Mode, InFile, OutFile])),
    Port = open_port({spawn, Cmd}, [exit_status, stderr_to_stdout, binary]),
    {ExitCode, Output} = collect_port(Port, <<>>),
    case ExitCode of
        0 ->
            {ok, GoBytes} = file:read_file(OutFile),
            Decoded = erlang:binary_to_term(GoBytes),
            case Expected =:= Decoded of
                true ->
                    io:format("[PASS] ~s~n", [Name]),
                    ok;
                false ->
                    io:format("[FAIL] ~s: term mismatch~n  want   = ~p~n  got    = ~p~n", [Name, Expected, Decoded]),
                    fail
            end;
        _ ->
            io:format("[FAIL] ~s: go binary exited ~p: ~s~n", [Name, ExitCode, Output]),
            fail
    end.

find_bin() ->
    case os:getenv("ETF_ROUNDTRIP_BIN") of
        false -> "/usr/local/bin/etf-roundtrip";
        Path -> Path
    end.

run_case(Bin, {Name, Mode, Term, Opts}) ->
    InFile = "/tmp/etf_in_" ++ Name ++ ".bin",
    OutFile = "/tmp/etf_out_" ++ Name ++ ".bin",
    Encoded = erlang:term_to_binary(Term, Opts),
    ok = file:write_file(InFile, Encoded),
    Cmd = lists:flatten(io_lib:format("~s ~s ~s ~s", [Bin, Mode, InFile, OutFile])),
    Port = open_port({spawn, Cmd}, [exit_status, stderr_to_stdout, binary]),
    {ExitCode, Output} = collect_port(Port, <<>>),
    case ExitCode of
        0 ->
            {ok, GoBytes} = file:read_file(OutFile),
            Decoded = erlang:binary_to_term(GoBytes),
            case Term =:= Decoded of
                true ->
                    io:format("[PASS] ~s~n", [Name]),
                    ok;
                false ->
                    io:format("[FAIL] ~s: term mismatch~n  orig   = ~p~n  got    = ~p~n", [Name, Term, Decoded]),
                    fail
            end;
        _ ->
            io:format("[FAIL] ~s: go binary exited ~p: ~s~n", [Name, ExitCode, Output]),
            fail
    end.

collect_port(Port, Acc) ->
    receive
        {Port, {data, Data}} ->
            collect_port(Port, <<Acc/binary, Data/binary>>);
        {Port, {exit_status, Status}} ->
            {Status, Acc}
    end.
